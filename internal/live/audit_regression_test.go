package live

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
)

func TestSignedURLNeverLeaksIntoErrorsOrRoomState(t *testing.T) {
	mgr, _, _, srv, _ := setupLiveTestHarness(t, http.NotFoundHandler())
	// Close server immediately so Do() fails with a *url.Error carrying the full signed URL.
	srv.Close()

	signedBili := srv.URL + "/live/stream.m3u8?sign=BILI_SECRET_SIG&expires=1999999999&token=BILI_TOK"
	_, err := mgr.fetchLiveLimitedBytes(context.Background(), signedBili, maxM3U8PlaylistBytes)
	if err == nil {
		t.Fatal("expected network error from closed server")
	}
	for _, secret := range []string{"BILI_SECRET_SIG", "BILI_TOK", "expires=1999999999", "sign="} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("fetchLiveLimitedBytes leaked %q in error: %v", secret, err)
		}
	}

	signedDouyin := srv.URL + "/live/stream.flv?x-expires=1999999999&upsig=DY_SECRET_SIG&sessionid=DY_TOK"
	_, flvErr := OpenHTTPFLVStream(context.Background(), site.DouyinLiveTransport("Mozilla/5.0 Test", true), []string{signedDouyin}, time.Second)
	if flvErr == nil {
		t.Fatal("expected network error from OpenHTTPFLVStream on closed server")
	}
	for _, secret := range []string{"DY_SECRET_SIG", "DY_TOK", "upsig=", "x-expires="} {
		if strings.Contains(flvErr.Error(), secret) {
			t.Fatalf("OpenHTTPFLVStream leaked %q in error: %v", secret, flvErr)
		}
	}

	rawURLErr := &url.Error{
		Op:  "Get",
		URL: "https://cdn.example.invalid/live/a.flv?sign=RAW_SECRET&token=RAW_TOK",
		Err: errors.New("connection reset by peer"),
	}
	sess := &recordSession{}
	mgr.setSessionStop(sess, "network", rawURLErr)
	if strings.Contains(sess.lastErr, "RAW_SECRET") || strings.Contains(sess.lastErr, "RAW_TOK") || strings.Contains(sess.lastErr, "cdn.example.invalid") {
		t.Fatalf("setSessionStop leaked URL/secret into lastErr: %q", sess.lastErr)
	}
	if !strings.Contains(sess.lastErr, "connection reset by peer") {
		t.Fatalf("expected underlying reason preserved, got %q", sess.lastErr)
	}
}

func TestLiveCoverWriteOrderOwnershipAndSignedDouyinCover(t *testing.T) {
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9}
	const sigQuery = "x-expires=1999999999&token=COVER_SIG_SECRET"

	mux := http.NewServeMux()
	mux.HandleFunc("/bili/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBytes)
	})
	mux.HandleFunc("/dy/signed-cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != sigQuery {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBytes)
	})
	mux.HandleFunc("/dy/huge-cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBytes[:3])
		chunk := bytes.Repeat([]byte{0xAA}, 1<<20)
		for i := 0; i < 17; i++ {
			_, _ = w.Write(chunk)
		}
	})

	mgr, lib, _, srv, _ := setupLiveTestHarness(t, mux)

	// 1. Bilibili saveLiveCover writes cover.jpg BEFORE saving item.json (never persists 0-byte cover).
	sess := regressionLiveSession(t, lib)
	mgr.mu.Lock()
	mgr.states[sess.roomID] = RoomState{RoomID: sess.roomID, Cover: srv.URL + "/bili/cover.jpg?" + sigQuery}
	mgr.mu.Unlock()

	var saveCalls atomic.Int32
	var badWriteOrder atomic.Bool
	lib.OnSaved(func(meta library.ItemMetadata) {
		saveCalls.Add(1)
		for _, mf := range meta.Media {
			if mf.Role == "cover" {
				info, err := os.Stat(filepath.Join(meta.ItemDir, mf.Path))
				if err != nil || info.Size() == 0 || mf.Bytes == 0 || mf.SHA256 == "" {
					badWriteOrder.Store(true)
				}
			}
		}
	})
	if err := mgr.saveLiveCover(context.Background(), sess); err != nil {
		t.Fatalf("saveLiveCover: %v", err)
	}
	if badWriteOrder.Load() || saveCalls.Load() != 1 {
		t.Fatalf("cover write order violated: badWriteOrder=%v saveCalls=%d", badWriteOrder.Load(), saveCalls.Load())
	}
	rawMeta, _ := os.ReadFile(filepath.Join(sess.itemDir, "item.json"))
	if strings.Contains(string(rawMeta), "COVER_SIG_SECRET") {
		t.Fatalf("signed cover query leaked into item.json: %s", string(rawMeta))
	}

	// 2. Douyin saveDouyinLiveCover protects pre-existing unowned cover.jpg.
	dyDir := filepath.Join(lib.CacheDir(), "douyin", "unowned-cover-test")
	if err := os.MkdirAll(dyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existingCover := filepath.Join(dyDir, "cover.jpg")
	if err := os.WriteFile(existingCover, []byte("USER_OWNED_PHOTO"), 0o600); err != nil {
		t.Fatal(err)
	}
	dyMeta, err := lib.SaveItem(dyDir, library.ItemMetadata{
		Site: "douyin", Kind: "live", ID: "live:7400991", Status: "recording",
		Live: &library.LiveRecordingInfo{RoomIDStr: "7400991"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dySess := &recordSession{
		siteID:   string(site.Douyin),
		itemDir:  dyDir,
		itemMeta: dyMeta,
		coverURL: srv.URL + "/dy/signed-cover.jpg?" + sigQuery,
	}
	transport := site.DouyinLiveTransport("Mozilla/5.0 Test", true)
	if err := mgr.saveDouyinLiveCover(context.Background(), dySess, transport); err == nil {
		t.Fatal("expected storage error when unowned cover.jpg exists")
	}
	if got, _ := os.ReadFile(existingCover); string(got) != "USER_OWNED_PHOTO" {
		t.Fatalf("unowned cover.jpg was overwritten: %q", string(got))
	}

	// 3. Douyin saveDouyinLiveCover rejects >16 MiB cover overflow instead of silently truncating.
	hugeDir := filepath.Join(lib.CacheDir(), "douyin", "huge-cover-test")
	if err := os.MkdirAll(hugeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hugeMeta, _ := lib.SaveItem(hugeDir, library.ItemMetadata{
		Site: "douyin", Kind: "live", ID: "live:7400992", Status: "recording",
		Live: &library.LiveRecordingInfo{RoomIDStr: "7400992"},
	})
	hugeSess := &recordSession{
		siteID:   string(site.Douyin),
		itemDir:  hugeDir,
		itemMeta: hugeMeta,
		coverURL: srv.URL + "/dy/huge-cover.jpg",
	}
	_ = mgr.saveDouyinLiveCover(context.Background(), hugeSess, transport)
	if _, statErr := os.Stat(filepath.Join(hugeDir, "cover.jpg")); !os.IsNotExist(statErr) {
		t.Fatalf("oversized cover (>16 MiB) should be rejected, statErr=%v", statErr)
	}

	// 4. Signed Douyin cover uses unstripped in-memory status.Cover (succeeds on 403-without-query CDN)
	// while RoomState.Cover and item.json remain stripped.
	dyProv := newFakeDouyinLiveProvider()
	const secUID = "MS4wLjABAAAA_signed_cover_dy"
	dyProv.statusFn = func(uid, _ string, _ int) (site.LiveStatus, error) {
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: secUID, RoomID: "7400993", WebRID: "99301",
			Status: site.LiveStateLive, Title: "签名封面测试", Cover: srv.URL + "/dy/signed-cover.jpg?" + sigQuery,
		}, nil
	}
	mgr.RegisterSiteProvider(dyProv)
	st, err := mgr.CheckAccountLiveStatus(context.Background(), site.Douyin, secUID, "99301", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.Cover, "COVER_SIG_SECRET") {
		t.Fatalf("RoomState.Cover leaked signed query: %q", st.Cover)
	}
	mgr.mu.Lock()
	rawCover := mgr.douyinRawCovers[secUID]
	mgr.mu.Unlock()
	if !strings.Contains(rawCover, sigQuery) {
		t.Fatalf("expected unstripped cover cached in memory, got %q", rawCover)
	}
	okDir := filepath.Join(lib.CacheDir(), "douyin", "ok-cover-test")
	_ = os.MkdirAll(okDir, 0o700)
	okMeta, _ := lib.SaveItem(okDir, library.ItemMetadata{
		Site: "douyin", Kind: "live", ID: "live:7400993", Status: "recording",
		Live: &library.LiveRecordingInfo{RoomIDStr: "7400993"},
	})
	okSess := &recordSession{
		siteID:   string(site.Douyin),
		itemDir:  okDir,
		itemMeta: okMeta,
		coverURL: rawCover,
	}
	if err := mgr.saveDouyinLiveCover(context.Background(), okSess, transport); err != nil {
		t.Fatalf("saveDouyinLiveCover with signed query failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(okDir, "cover.jpg")); err != nil || !bytes.Equal(got, jpegBytes) {
		t.Fatalf("signed cover not saved: len=%d err=%v", len(got), err)
	}
}

func TestDiskFreeWalksUpAndRecordersRecreateDeletedItemDir(t *testing.T) {
	missingNested := filepath.Join(t.TempDir(), "removed-by-rclone", "sub", "itemDir")
	free, err := getFreeDiskBytes(missingNested)
	if err != nil || free == 0 {
		t.Fatalf("getFreeDiskBytes on non-existent nested dir failed: free=%d err=%v", free, err)
	}

	initBytes, fragBytes := splitSyntheticHLSInitAndFrag(t)
	flvBytes := buildSyntheticFLVBytes(t, "avc", 35)
	mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index":
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/init\"\n#EXTINF:1,\n/frag\n#EXT-X-ENDLIST\n"))
		case "/init":
			_, _ = w.Write(initBytes)
		case "/frag":
			_, _ = w.Write(fragBytes)
		case "/live.flv":
			w.Header().Set("Content-Type", "video/x-flv")
			_, _ = w.Write(flvBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	mgr.minFreeBytes = 1
	mgr.freeBytesFunc = getFreeDiskBytes

	// Remove itemDir completely (simulating rclone move removing emptied dir) before HLS and FLV writes.
	sessHLS := regressionLiveSession(t, lib)
	if err := os.RemoveAll(sessHLS.itemDir); err != nil {
		t.Fatal(err)
	}
	if n, err := mgr.recordHLSSegment(context.Background(), sessHLS, []string{base + "/index"}, filepath.Join(sessHLS.itemDir, "part-001.mp4.part"), true); err != nil || n == 0 {
		t.Fatalf("HLS recording failed after empty itemDir removal: n=%d err=%v", n, err)
	}

	sessFLV := regressionLiveSession(t, lib)
	if err := os.RemoveAll(sessFLV.itemDir); err != nil {
		t.Fatal(err)
	}
	var conn *activeFLVConn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	if n, err := mgr.recordFLVPartWithTransport(context.Background(), sessFLV, site.DouyinLiveTransport("Mozilla/5.0 Test", true), []string{base + "/live.flv"}, &conn, filepath.Join(sessFLV.itemDir, "part-001.mp4.part")); err != nil || n == 0 {
		t.Fatalf("FLV recording failed after empty itemDir removal: n=%d err=%v", n, err)
	}
}

func TestOpenHTTPFLVStreamIdleTimeoutUsesSharedHelper(t *testing.T) {
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/stall.flv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	_, _, _, srv, _ := setupLiveTestHarness(t, mux)
	defer close(release)

	rc, err := OpenHTTPFLVStream(context.Background(), site.DouyinLiveTransport("Mozilla/5.0 Test", true), []string{srv.URL + "/stall.flv"}, 80*time.Millisecond)
	if err != nil {
		t.Fatalf("OpenHTTPFLVStream: %v", err)
	}
	defer rc.Close()

	buf := make([]byte, 64)
	start := time.Now()
	_, readErr := rc.Read(buf)
	if readErr == nil || !site.IsKind(readErr, site.ErrKindNetwork) {
		t.Fatalf("expected network idle timeout error, got %v", readErr)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("idle read timeout took too long: %v", elapsed)
	}
}

func TestFLVRemuxLongGOPNotKilledAsAudioOnlyAndMetadataType4(t *testing.T) {
	sps := mustHex(t, testAVCSPSHex)
	pps := mustHex(t, testAVCPPSHex)
	idr := mustHex(t, testAVCSliceHex)
	pSlice := mustHex(t, testAVCPSlice)

	// 1. Verify raw Enhanced FLV PacketType=4 (Metadata) is ignored cleanly.
	if flvExPacketMetadata != 4 {
		t.Fatalf("flvExPacketMetadata = %d, want 4", flvExPacketMetadata)
	}
	metaTag := FLVTag{
		Type:      flvTagTypeVideo,
		Timestamp: 0,
		Data:      []byte{0x80 | (1 << 4) | 4, 'h', 'v', 'c', '1', 0x01, 0x02},
	}
	parsedMeta, err := ParseFLVTag(metaTag)
	if err != nil || parsedMeta.Kind != ParsedTagIgnored {
		t.Fatalf("Enhanced FLV PacketType=4 metadata rejected: parsed=%+v err=%v", parsedMeta, err)
	}

	// 2. Video+audio stream with >3s GOP (4.5s of P-frames + audio before first keyframe) MUST NOT fail as audio-only.
	fb := newFLVBuilder(0x05)
	fb.writeClassicVideoConfig(0, flvVideoCodecAVC, buildAVCDecConfBytes(t, sps, pps, 4))
	fb.writeAACConfig(0, testAACConfig48kStereo)

	// 4.5s of non-keyframe video + audio before first IDR at 4500ms.
	for ms := uint32(0); ms < 4500; ms += 40 {
		fb.writeAACFrame(ms, testAACSilentFrame)
		fb.writeClassicVideoFrame(ms, flvVideoCodecAVC, false, 0, packLengthPrefixedNALUs(4, pSlice))
	}
	// First IDR at 4500ms followed by 2 seconds of video+audio.
	for i := 0; i < 50; i++ {
		ts := uint32(4500 + i*40)
		fb.writeAACFrame(ts, testAACSilentFrame)
		isKey := i%25 == 0
		nalu := pSlice
		if isKey {
			nalu = idr
		}
		fb.writeClassicVideoFrame(ts, flvVideoCodecAVC, isKey, 0, packLengthPrefixedNALUs(4, nalu))
	}

	_, summary, _ := remuxSinglePartAndFinalize(t, fb.buf.Bytes())
	if !summary.MoovBeforeMdat || summary.TrackCount != 2 || summary.DurationSec < 1.8 {
		t.Fatalf("4.5s GOP stream failed to remux cleanly: %+v", summary)
	}
}

func TestFLVRemuxOutOfWindowInitialAudioDoesNotLoopForever(t *testing.T) {
	sps := mustHex(t, testAVCSPSHex)
	pps := mustHex(t, testAVCPPSHex)
	idr := mustHex(t, testAVCSliceHex)
	pSlice := mustHex(t, testAVCPSlice)

	fb := newFLVBuilder(0x05)
	fb.writeClassicVideoConfig(0, flvVideoCodecAVC, buildAVCDecConfBytes(t, sps, pps, 4))
	fb.writeAACConfig(0, testAACConfig48kStereo)
	// First video keyframe at 1000ms.
	fb.writeClassicVideoFrame(1000, flvVideoCodecAVC, true, 0, packLengthPrefixedNALUs(4, idr))
	// Out-of-window initial audio frames (>10s ahead of keyframe) must be dropped, not trigger rollover loop!
	fb.writeAACFrame(30000, testAACSilentFrame)
	fb.writeAACFrame(30021, testAACSilentFrame)
	// Subsequent normal frames aligned with video.
	for i := 1; i < 45; i++ {
		ts := uint32(1000 + i*40)
		fb.writeAACFrame(ts, testAACSilentFrame)
		isKey := i%25 == 0
		nalu := pSlice
		if isKey {
			nalu = idr
		}
		fb.writeClassicVideoFrame(ts, flvVideoCodecAVC, isKey, 0, packLengthPrefixedNALUs(4, nalu))
	}

	dir := t.TempDir()
	parts, err := RemuxFLVStream(
		context.Background(),
		bytes.NewReader(fb.buf.Bytes()),
		FLVRemuxOptions{},
		func(partIndex int) (PartTarget, func(FLVCheckpoint, error) error, error) {
			if partIndex > 3 {
				return nil, nil, errors.New("infinite rollover loop detected")
			}
			p := filepath.Join(dir, fmt.Sprintf("part-%03d.mp4.part", partIndex))
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
			if err != nil {
				return nil, nil, err
			}
			return f, func(_ FLVCheckpoint, _ error) error { return f.Close() }, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("RemuxFLVStream failed: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part without rollover loop, got %d parts", len(parts))
	}
}

func TestFinalizeLivePartAcceptsCrossTrackLateAudioOffset(t *testing.T) {
	sps, _ := hex.DecodeString(testAVCSPSHex)
	pps, _ := hex.DecodeString(testAVCPPSHex)
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
	audioSample := testAACSilentFrame
	syncFlags := mp4.SampleFlags{SampleDependsOn: 2}.Encode()

	// Video runs [0s .. 2.0s]; audio starts late at 1.8s and runs [1.8s .. 3.8s] -> combined timeline ~3.8s!
	vf, _ := mp4.CreateFragment(1, 1)
	for i := 0; i < 60; i++ {
		vf.AddFullSample(mp4.FullSample{
			Sample:     mp4.Sample{Flags: syncFlags, Dur: 1000, Size: uint32(len(videoSample))},
			DecodeTime: uint64(i) * 1000,
			Data:       videoSample,
		})
	}
	if err := vf.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	af, _ := mp4.CreateFragment(2, 2)
	audioStart := uint64(86400) // 1.8s at 48kHz
	for i := 0; i < 94; i++ {
		af.AddFullSample(mp4.FullSample{
			Sample:     mp4.Sample{Flags: syncFlags, Dur: 1024, Size: uint32(len(audioSample))},
			DecodeTime: audioStart + uint64(i)*1024,
			Data:       audioSample,
		})
	}
	if err := af.Encode(&buf); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	partPath := filepath.Join(dir, "part-001.mp4.part")
	finalPath := filepath.Join(dir, "part-001.mp4")
	if err := os.WriteFile(partPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := finalizeLivePart(context.Background(), partPath, finalPath)
	if err != nil {
		t.Fatalf("finalizeLivePart rejected valid recording with >1s late audio offset: %v", err)
	}
	if !summary.MoovBeforeMdat || summary.DurationSec < 3.5 {
		t.Fatalf("unexpected summary for late-audio recording: %+v", summary)
	}
}

func TestReconnectInnerRetryNeverReusesExpiredDouyinURL(t *testing.T) {
	const secUID = "MS4wLjABAAAA_reconnect_retry_dy"
	const roomID = "7400000000000008801"
	flv := buildSyntheticFLVBytes(t, "avc", 35)

	var v1Hits, v2Hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/live/v1.flv", func(w http.ResponseWriter, r *http.Request) {
		v1Hits.Add(1)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(flv)
	})
	mux.HandleFunc("/live/v2.flv", func(w http.ResponseWriter, r *http.Request) {
		v2Hits.Add(1)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(flv)
	})

	mgr, _, _, srv, _ := setupLiveTestHarness(t, mux)
	prov := newFakeDouyinLiveProvider()
	transport := site.DouyinLiveTransport("Mozilla/5.0 Test", true)

	prov.statusFn = func(uid, _ string, _ int) (site.LiveStatus, error) {
		st := site.LiveStateLive
		if v2Hits.Load() >= 1 {
			st = site.LiveStateOffline
		}
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: secUID, RoomID: roomID, WebRID: "880101",
			Status: st, Title: "重连不复用旧URL", LiveSince: 1794024000,
		}, nil
	}
	var selectCalls atomic.Int32
	prov.streamFn = func(uid, _ string, _ []string, _ int) (site.LiveStreamPlan, error) {
		n := selectCalls.Add(1)
		switch n {
		case 1:
			return site.LiveStreamPlan{
				Site: site.Douyin, AccountUID: secUID, RoomID: roomID, WebRID: "880101",
				Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV, Format: "flv", Codec: "avc", QualityName: "原画",
				URLs: []string{srv.URL + "/live/v1.flv"}, Media: transport,
			}, nil
		case 2, 3:
			// Transient errors during reconnect must stay in inner retry loop, NEVER reusing v1.flv!
			return site.LiveStreamPlan{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindNetwork, Message: "临时网络错误"}
		default:
			return site.LiveStreamPlan{
				Site: site.Douyin, AccountUID: secUID, RoomID: roomID, WebRID: "880101",
				Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV, Format: "flv", Codec: "avc", QualityName: "原画",
				URLs: []string{srv.URL + "/live/v2.flv"}, Media: transport,
			}, nil
		}
	}
	mgr.RegisterSiteProvider(prov)

	if _, err := mgr.StartDouyinRecording(context.Background(), secUID, "880101"); err != nil {
		t.Fatalf("StartDouyinRecording: %v", err)
	}
	done := waitUntilRoomIdle(t, mgr, "douyin:live:"+roomID, 6*time.Second)
	if v1Hits.Load() != 1 || v2Hits.Load() != 1 || done.RecordSegments != 2 {
		t.Fatalf("expired URL reused or reconnect failed: v1Hits=%d v2Hits=%d segments=%d stop=%q", v1Hits.Load(), v2Hits.Load(), done.RecordSegments, done.StopReason)
	}
}

func TestObserveTransferSpeedVBRNotFlaggedSlow(t *testing.T) {
	mgr, _, _, _, _ := setupLiveTestHarness(t, http.NotFoundHandler())
	sess := &recordSession{
		roomID:          9901,
		slow:            newSlowDetector(),
		nominalByteRate: 1_000_000, // 8 Mbps nominal
	}
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	// Healthy VBR stream: 200 KB for 2s of media downloaded in 1.0s (2x real-time, far below 8 Mbps nominal).
	for i := 0; i < 40; i++ {
		start := base.Add(time.Duration(i) * time.Second)
		end := start.Add(time.Second)
		mgr.observeTransferSpeed(sess, start, end, 200_000, 2.0, "part-001.mp4.part")
	}
	if len(sess.incidents) != 0 {
		t.Fatalf("healthy real-time VBR stream falsely flagged slow: %+v", sess.incidents)
	}

	// Fallback when mediaDurationSec == 0: rate below 0.9 * nominal for >=30s triggers slow.
	for i := 0; i < 35; i++ {
		start := base.Add(time.Duration(100+i) * time.Second)
		end := start.Add(time.Second)
		mgr.observeTransferSpeed(sess, start, end, 200_000, 0, "part-001.mp4.part")
	}
	if len(sess.incidents) != 1 || sess.incidents[0].Kind != library.LiveIncidentSlow {
		t.Fatalf("expected nominalByteRate fallback slow incident when mediaDurationSec<=0, got %+v", sess.incidents)
	}
}

func TestShortSegmentClampsDurationAndRecoverySkipsMovedShortSegment(t *testing.T) {
	if got := roundedLiveDurationSec(0.2); got != 1 {
		t.Fatalf("roundedLiveDurationSec(0.2) = %d, want 1", got)
	}
	if got := roundedLiveDurationSec(0); got != 0 {
		t.Fatalf("roundedLiveDurationSec(0) = %d, want 0", got)
	}

	mgr, lib, _ := regressionRecorder(t, http.NotFoundHandler())
	lib.SetAllowMovedFiles(true)
	dir := filepath.Join(lib.CacheDir(), "bilibili", "short-moved")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Legacy finalized <0.5s segment with Duration=0, SHA256 set, Live.Segments DurationSec=0.35, moved away.
	saved, err := lib.SaveItem(dir, library.ItemMetadata{
		Site:   "bilibili",
		Kind:   "live",
		ID:     "live:555:100",
		Status: "complete",
		Media: []library.MediaFile{{
			Role: "live_segment", Path: "part-001.mp4", Bytes: 12345, SHA256: "deadbeef", Duration: 0,
		}},
		Live: &library.LiveRecordingInfo{
			RoomID: 555, LiveSince: 100, StopReason: "ended",
			Segments: []library.LiveSegmentInfo{{File: "part-001.mp4", Bytes: 12345, SHA256: "deadbeef", DurationSec: 0.35}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	mgr.RecoverOrphanParts()

	got, ok, err := lib.GetItem(saved.ItemKey)
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if got.Status != "complete" || len(got.PendingParts) != 0 || len(got.Live.Incidents) != 0 {
		t.Fatalf("RecoverOrphanParts corrupted moved short segment: %+v", got)
	}
}

func TestDouyinStatusCacheInvalidationOfflinePreserveAndCacheHitAutoStart(t *testing.T) {
	const secUID = "MS4wLjABAAAA_cache_truth_dy"
	const roomID = "7400000000000007701"
	flv := buildSyntheticFLVBytes(t, "avc", 35)

	mux := http.NewServeMux()
	mux.HandleFunc("/live/stream.flv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(flv)
	})
	mgr, _, subMgr, srv, _ := setupLiveTestHarness(t, mux)
	prov := newFakeDouyinLiveProvider()
	transport := site.DouyinLiveTransport("Mozilla/5.0 Test", true)

	var online atomic.Bool
	online.Store(true)
	prov.statusFn = func(uid, _ string, _ int) (site.LiveStatus, error) {
		if online.Load() {
			return site.LiveStatus{
				Site: site.Douyin, AccountUID: secUID, RoomID: roomID, WebRID: "770101",
				Status: site.LiveStateLive, Title: "开播中", LiveSince: 1794024000,
			}, nil
		}
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: secUID, WebRID: "770101",
			Status: site.LiveStateOffline,
		}, nil
	}
	prov.streamFn = func(uid, _ string, _ []string, _ int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: secUID, RoomID: roomID, WebRID: "770101",
			Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV, Format: "flv", Codec: "avc", QualityName: "原画",
			URLs: []string{srv.URL + "/live/stream.flv"}, Media: transport,
		}, nil
	}
	mgr.RegisterSiteProvider(prov)

	// 1. Prime cache while account is NOT auto-record.
	if _, err := mgr.CheckAccountLiveStatus(context.Background(), site.Douyin, secUID, "770101", true); err != nil {
		t.Fatal(err)
	}
	// Seed incidents + RecordItemDir on douyin:live:<roomID>, then switch to offline (douyin:user:<secUID>).
	mgr.mu.Lock()
	st := mgr.douyinStates["douyin:live:"+roomID]
	st.RecordItemDir = filepath.Join("synthetic", "dir", roomID)
	st.StopReason = "ended"
	st.LastError = "last-err"
	st.Incidents = []library.LiveIncident{{Kind: library.LiveIncidentReconnect, At: "2026-11-20T00:00:00Z", Count: 1}}
	mgr.douyinStates["douyin:live:"+roomID] = st
	mgr.mu.Unlock()

	online.Store(false)
	offSt, err := mgr.CheckAccountLiveStatus(context.Background(), site.Douyin, secUID, "770101", true)
	if err != nil {
		t.Fatal(err)
	}
	if offSt.RoomKey != "douyin:user:"+secUID || offSt.RecordItemDir != st.RecordItemDir || len(offSt.Incidents) != 1 || offSt.StopReason != "ended" {
		t.Fatalf("offline transition lost RecordItemDir/Incidents/StopReason: %+v", offSt)
	}

	// 2. ClearIdleIncidents must immediately reflect in CachedAccountState (not stale douyinStatusCache).
	if !mgr.ClearIdleIncidents("douyin", "", secUID) {
		t.Fatal("expected ClearIdleIncidents to clear offline Douyin incidents")
	}
	cachedAfterClear, ok := mgr.CachedAccountState(site.Douyin, secUID)
	if !ok || len(cachedAfterClear.Incidents) != 0 {
		t.Fatalf("CachedAccountState returned stale incidents after ClearIdleIncidents: %+v", cachedAfterClear)
	}

	// 3. Cache hit in CheckAccountLiveStatus evaluates autoStart when AutoRecord is enabled!
	online.Store(true)
	if _, err := mgr.CheckAccountLiveStatus(context.Background(), site.Douyin, secUID, "770101", true); err != nil {
		t.Fatal(err)
	}
	callsBefore := prov.statusCallCount(secUID)
	// Enable AutoRecord subscription now, then refresh WITHOUT force (cache hit!).
	if _, err := subMgr.Create("自动录播", "", []subscription.AccountRef{{Site: "douyin", UID: secUID, Name: "抖音主播"}}, subscription.SubRules{AutoRecord: true}); err != nil {
		t.Fatal(err)
	}
	online.Store(false) // so recording finishes cleanly after 1 part
	if _, err := mgr.CheckAccountLiveStatus(context.Background(), site.Douyin, secUID, "770101", false); err != nil {
		t.Fatal(err)
	}
	if prov.statusCallCount(secUID) != callsBefore {
		t.Fatalf("expected cache hit with 0 extra status calls, before=%d after=%d", callsBefore, prov.statusCallCount(secUID))
	}
	if prov.streamCallCount("770101") == 0 {
		t.Fatal("cache hit in CheckAccountLiveStatus skipped autoStart")
	}
	waitUntilRoomIdle(t, mgr, "douyin:live:"+roomID, 5*time.Second)

	// After session ended with StopReason="ended", CachedAccountState must report offline (not stuck live).
	cachedEnded, ok := mgr.CachedAccountState(site.Douyin, secUID)
	if !ok || cachedEnded.LiveStatus != 0 {
		t.Fatalf("CachedAccountState stuck live after session ended: ok=%v st=%+v", ok, cachedEnded)
	}
}

func TestRefreshForAccountsFiltersStrictlyToRequestedBilibiliUIDs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"4001": map[string]any{"uid": 4001, "room_id": 9401, "title": "Live 1", "uname": "U4001", "live_status": 1, "live_time": 1794024000},
				"4002": map[string]any{"uid": 4002, "room_id": 9402, "title": "Offline 2", "uname": "U4002", "live_status": 0},
			},
		})
	})
	mgr, _, _, _, _ := setupLiveTestHarness(t, mux)

	// Pre-populate another live Bilibili room and a live Douyin room.
	mgr.mu.Lock()
	mgr.states[9401] = RoomState{Site: "bilibili", AccountUID: "4001", UID: 4001, RoomID: 9401, RoomIDStr: "9401", RoomKey: "bilibili:live:9401", LiveStatus: 1}
	mgr.douyinStates["douyin:live:7400999"] = RoomState{Site: "douyin", AccountUID: "sec_other", RoomIDStr: "7400999", RoomKey: "douyin:live:7400999", LiveStatus: 1}
	mgr.mu.Unlock()

	rooms, err := mgr.RefreshForAccounts(context.Background(), []int64{4002})
	if err != nil {
		t.Fatalf("RefreshForAccounts: %v", err)
	}
	if len(rooms) != 1 || rooms[0].Site != "bilibili" || rooms[0].UID != 4002 || rooms[0].RoomID != 9402 {
		t.Fatalf("RefreshForAccounts returned unrelated rooms: %+v", rooms)
	}
}

func TestEmptyTargetAndPrefixGuardsNeverStopWrongRecordingOrMatchIdleRoom(t *testing.T) {
	const secUID = "MS4wLjABAAAA_empty_guard_dy"
	const roomID = "7400000000000006601"
	flv := buildSyntheticFLVBytes(t, "avc", 45)
	holdDone := make(chan struct{})
	started := make(chan struct{}, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/live/hold.flv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(flv)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-holdDone:
		case <-r.Context().Done():
		}
	})
	mgr, _, _, srv, _ := setupLiveTestHarness(t, mux)
	prov := newFakeDouyinLiveProvider()
	transport := site.DouyinLiveTransport("Mozilla/5.0 Test", true)
	prov.statusFn = func(uid, _ string, _ int) (site.LiveStatus, error) {
		return site.LiveStatus{Site: site.Douyin, AccountUID: secUID, RoomID: roomID, WebRID: "", Status: site.LiveStateLive, Title: "无WebRID直播"}, nil
	}
	prov.streamFn = func(uid, _ string, _ []string, _ int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: secUID, RoomID: roomID, WebRID: "",
			Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV, Format: "flv", Codec: "avc",
			URLs: []string{srv.URL + "/live/hold.flv"}, Media: transport,
		}, nil
	}
	mgr.RegisterSiteProvider(prov)

	// Add idle Bilibili and Douyin rooms with empty RecordSession / WebRID.
	mgr.mu.Lock()
	mgr.states[9111] = RoomState{Site: "bilibili", UID: 111, RoomID: 9111}
	mgr.douyinStates["douyin:user:idle_sec"] = RoomState{Site: "douyin", AccountUID: "idle_sec"}
	mgr.mu.Unlock()

	if _, err := mgr.StartDouyinRecording(context.Background(), secUID, roomID); err != nil {
		t.Fatalf("StartDouyinRecording: %v", err)
	}
	<-started

	// Empty / malformed keys must error and must NOT stop the active Douyin session with empty webRID.
	if _, err := mgr.StopDouyinRecording(""); err == nil {
		t.Fatal("expected error for StopDouyinRecording(\"\")")
	}
	if _, err := mgr.StopRecordingByKey(""); err == nil {
		t.Fatal("expected error for StopRecordingByKey(\"\")")
	}
	if _, err := mgr.StopRecordingByKey("bilibili:live:invalid"); err == nil {
		t.Fatal("expected error for StopRecordingByKey(\"bilibili:live:invalid\")")
	}
	if _, err := mgr.StopRecording(0); err == nil {
		t.Fatal("expected error for StopRecording(0)")
	}
	if _, err := mgr.StopRecordingByKey("bilibili:user:999999"); err != nil {
		t.Fatalf("StopRecordingByKey(bilibili:user:999999) unexpected error: %v", err)
	}

	// Active Douyin session must still be running!
	if st, ok := mgr.CachedAccountState(site.Douyin, secUID); !ok || !st.Recording {
		t.Fatalf("active Douyin recording was stopped by empty/unrelated key: ok=%v st=%+v", ok, st)
	}

	// RecordingPreview with bare prefixes must NOT match idle rooms.
	for _, badKey := range []string{"", "   ", "live:", "douyin:", "douyin:live:", "douyin:user:", "bilibili:", "bilibili:live:"} {
		if pv, ok := mgr.RecordingPreview(badKey); ok {
			t.Fatalf("RecordingPreview(%q) unexpectedly matched %+v", badKey, pv)
		}
	}

	close(holdDone)
	if _, err := mgr.StopDouyinRecording(roomID); err != nil {
		t.Fatalf("StopDouyinRecording(%s): %v", roomID, err)
	}
}

func TestCancelRecordingsPreservesStopReasonAndReentrantStopForSiteNoDeadlock(t *testing.T) {
	initBytes, fragBytes := splitSyntheticHLSInitAndFrag(t)
	var hits atomic.Int32
	var srvURL string
	var mgrRef atomic.Pointer[Manager]
	var reentrantDone atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"6001": map[string]any{"uid": 6001, "room_id": 9601, "title": "Reentrant", "uname": "U6001", "live_status": 1, "live_time": 1794024000},
		}})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		// On reconnect (call >= 2), trigger -101 auth expired which synchronously invokes StopForSite on the recording goroutine!
		if hits.Load() >= 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": -101, "message": "账号未登录"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"room_id": 9601, "uid": 6001, "live_status": 1, "live_time": 1794024000,
		}})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"room_id": 9601, "uid": 6001, "live_status": 1,
			"playurl_info": map[string]any{"playurl": map[string]any{"stream": []map[string]any{{
				"protocol_name": "http_hls",
				"format": []map[string]any{{"format_name": "fmp4", "codec": []map[string]any{{
					"codec_name": "avc", "current_qn": 10000, "base_url": "/hls/index.m3u8",
					"url_info": []map[string]any{{"host": srvURL, "extra": ""}},
				}}}},
			}}}},
		}})
	})
	mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1,\n/hls/seg1.m4s\n"))
			return
		}
		w.WriteHeader(http.StatusBadGateway) // force reconnect -> RoomInit -> -101
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fragBytes) })

	mgr, _, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	mgrRef.Store(mgr)
	mgr.finalizeTimeout = 10 * time.Second

	// Register re-entrant StopForSite inside Bilibili client's AuthExpiredHandler (mirrors service/backend.go).
	mgr.client.SetAuthExpiredHandler(func() {
		if m := mgrRef.Load(); m != nil {
			m.StopForSite(site.Bilibili, "auth_required")
			reentrantDone.Store(true)
		}
	})

	start := time.Now()
	if _, err := mgr.StartRecording(context.Background(), 9601); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	done := waitUntilRoomIdle(t, mgr, "bilibili:live:9601", 4*time.Second)
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("re-entrant StopForSite deadlocked or took too long: %v", elapsed)
	}
	if !reentrantDone.Load() || done.StopReason != "auth_required" {
		t.Fatalf("expected re-entrant StopForSite to stop with auth_required: reentrant=%v state=%+v", reentrantDone.Load(), done)
	}

	// Verify CancelRecordings preserves an existing stopReason.
	dummyCtx, dummyCancel := context.WithCancel(context.Background())
	defer dummyCancel()
	s := &recordSession{roomID: 1234, stopReason: "user_stop", cancel: dummyCancel, doneCh: make(chan struct{})}
	mgr.mu.Lock()
	mgr.sessions[1234] = s
	mgr.mu.Unlock()
	mgr.CancelRecordings("app_closed")
	if s.stopReason != "user_stop" || dummyCtx.Err() == nil {
		t.Fatalf("CancelRecordings overwrote existing stopReason or failed to cancel: reason=%q err=%v", s.stopReason, dummyCtx.Err())
	}
	mgr.mu.Lock()
	delete(mgr.sessions, 1234)
	mgr.mu.Unlock()
}

func TestPerKeyStartDoesNotBlockStopOrOtherRoomsAndHintPrunesUserKey(t *testing.T) {
	roomABlocked := make(chan struct{})
	roomARelease := make(chan struct{})
	initBytes, fragBytes := splitSyntheticHLSInitAndFrag(t)
	var srvURL string

	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"room_id": 9702, "uid": 7002, "live_status": 1, "live_time": 1794024000,
		}})
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"7002": map[string]any{"uid": 7002, "room_id": 9702, "title": "Room B", "uname": "U7002", "live_status": 1, "live_time": 1794024000},
		}})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"room_id": 9702, "uid": 7002, "live_status": 1,
			"playurl_info": map[string]any{"playurl": map[string]any{"stream": []map[string]any{{
				"protocol_name": "http_hls",
				"format": []map[string]any{{"format_name": "fmp4", "codec": []map[string]any{{
					"codec_name": "avc", "current_qn": 10000, "base_url": "/hls/b.m3u8",
					"url_info": []map[string]any{{"host": srvURL, "extra": ""}},
				}}}},
			}}}},
		}})
	})
	mux.HandleFunc("/hls/b.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1,\n/hls/seg1.m4s\n"))
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fragBytes) })

	var stateEvents []RoomState
	var evMu sync.Mutex
	mgr, _, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	mgr.onState = func(st RoomState) {
		evMu.Lock()
		stateEvents = append(stateEvents, st)
		evMu.Unlock()
	}

	// Register a Douyin provider whose SelectLiveStream blocks until cancelled by Manager.Stop().
	blockingProv := &blockingDouyinProvider{blocked: roomABlocked, release: roomARelease}
	mgr.RegisterSiteProvider(blockingProv)

	// Start Douyin Room A in background (blocked in SelectLiveStream).
	roomADone := make(chan error, 1)
	go func() {
		_, err := mgr.StartDouyinRecording(context.Background(), "MS4wLjABAAAA_blocked_start_a", "970101")
		roomADone <- err
	}()
	<-roomABlocked

	// Unrelated Bilibili Room B must start immediately without being blocked by Room A!
	stB, err := mgr.StartRecording(context.Background(), 9702)
	if err != nil || !stB.Recording {
		close(roomARelease)
		t.Fatalf("unrelated Room B was blocked by Room A start: st=%+v err=%v", stB, err)
	}

	// ObserveAccountLiveHint prunes old douyin:user:<uid>, invalidates cache, and emits onState.
	const hintUID = "MS4wLjABAAAA_hint_prune"
	mgr.mu.Lock()
	mgr.douyinStates["douyin:user:"+hintUID] = RoomState{Site: "douyin", AccountUID: hintUID, RoomKey: "douyin:user:" + hintUID, LiveStatus: 0}
	mgr.douyinStatusCache[hintUID] = douyinCachedStatusEntry{fetchedAt: mgr.nowLocked()}
	mgr.mu.Unlock()

	mgr.ObserveAccountLiveHint(site.Douyin, hintUID, "55501", "740055501")
	mgr.mu.Lock()
	_, oldStillPresent := mgr.douyinStates["douyin:user:"+hintUID]
	_, cacheStillPresent := mgr.douyinStatusCache[hintUID]
	mgr.mu.Unlock()
	if oldStillPresent || cacheStillPresent {
		t.Fatalf("ObserveAccountLiveHint failed to prune old user key or cache: old=%v cache=%v", oldStillPresent, cacheStillPresent)
	}
	evMu.Lock()
	sawHintEvent := false
	for _, ev := range stateEvents {
		if ev.RoomKey == "douyin:live:740055501" && ev.LiveStatus == 1 {
			sawHintEvent = true
		}
	}
	evMu.Unlock()
	if !sawHintEvent {
		t.Fatal("ObserveAccountLiveHint did not emit onState")
	}

	// Stop() must cancel Room A's in-flight start and complete quickly (never blocked by Room A).
	stopDone := make(chan struct{})
	go func() {
		mgr.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(3 * time.Second):
		close(roomARelease)
		t.Fatal("Manager.Stop() blocked on slow StartRecording I/O")
	}
	select {
	case <-roomADone:
	case <-time.After(2 * time.Second):
		close(roomARelease)
		t.Fatal("Room A StartRecording was not cancelled by Manager.Stop()")
	}
}

func TestM3U8ParserEdgeCasesAndRecoveryDedupAndMultiSubORMerge(t *testing.T) {
	// 1. UTF-8 BOM + quoted URI containing comma + sticky EXT-X-KEY encryption + EXT-X-MAP BYTERANGE.
	p1 := parseM3U8PlaylistDetails("\uFEFF#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://cdn.example/k\"\n#EXT-X-KEY:METHOD=NONE\n#EXT-X-MAP:URI=\"https://cdn.example/a,b/init.mp4\"\n#EXTINF:2.0,\nseg1.m4s\n", "https://cdn.example/live/index.m3u8")
	if !p1.valid || !p1.encrypted || p1.mapURI != "https://cdn.example/a,b/init.mp4" {
		t.Fatalf("unexpected BOM/sticky-key/comma-URI parse: %+v", p1)
	}

	p2 := parseM3U8PlaylistDetails("#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\",BYTERANGE=\"720@0\"\n#EXTINF:1.0,\nseg1.m4s\n", "https://cdn.example/index.m3u8")
	if p2.valid {
		t.Fatalf("expected EXT-X-MAP BYTERANGE to invalidate playlist: %+v", p2)
	}

	p3 := parseM3U8PlaylistDetails("#EXTM3U\n#EXT-X-STREAM-INF:CODECS=\"avc1.640032,mp4a.40.2\",BANDWIDTH=5000000\nhigh.m3u8\n", "https://cdn.example/master.m3u8")
	if v, ok := p3.bestVariantInfo(); !ok || v.bandwidth != 5000000 {
		t.Fatalf("quoted CODECS comma broke BANDWIDTH parsing: v=%+v ok=%v", v, ok)
	}

	// 2. RecoverOrphanParts: no duplicate convert_failed across restarts, clears empty PendingParts & marks 0-segment "failed",
	// and backfills Codec without applying Bilibili quality fallback on Douyin.
	mgr, lib, _ := regressionRecorder(t, http.NotFoundHandler())
	corruptDir := filepath.Join(lib.CacheDir(), "bilibili", "corrupt-dedup")
	_ = os.MkdirAll(corruptDir, 0o700)
	_ = os.WriteFile(filepath.Join(corruptDir, "part-001.mp4.part"), []byte("corrupt-bytes"), 0o600)
	cMeta, _ := lib.SaveItem(corruptDir, library.ItemMetadata{
		Site: "bilibili", Kind: "live", ID: "live:801:1", Status: "recording",
		PendingParts: []string{"part-001.mp4.part"},
		Live:         &library.LiveRecordingInfo{RoomID: 801, LiveSince: 1},
	})
	mgr.RecoverOrphanParts()
	mgr.RecoverOrphanParts()
	gotCorrupt, _, _ := lib.GetItem(cMeta.ItemKey)
	if gotCorrupt.Live == nil || len(gotCorrupt.Live.Incidents) != 1 {
		t.Fatalf("RecoverOrphanParts duplicated convert_failed across restarts: %+v", gotCorrupt.Live)
	}

	emptyDir := filepath.Join(lib.CacheDir(), "douyin", "empty-pending")
	_ = os.MkdirAll(emptyDir, 0o700)
	eMeta, _ := lib.SaveItem(emptyDir, library.ItemMetadata{
		Site: "douyin", Kind: "live", ID: "live:802", Status: "recording",
		PendingParts: []string{"part-001.mp4.part"}, // crashed before .part file was created
		Live:         &library.LiveRecordingInfo{RoomIDStr: "802", PendingFile: "part-001.mp4.part", PendingBytes: 0},
	})
	mgr.RecoverOrphanParts()
	rawEmpty, err := os.ReadFile(filepath.Join(emptyDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	diskEmpty, err := library.ParseItemMetadata(rawEmpty)
	if err != nil {
		t.Fatal(err)
	}
	if len(diskEmpty.PendingParts) != 0 || diskEmpty.Status != "failed" || len(diskEmpty.Live.Incidents) != 0 {
		t.Fatalf("empty PendingParts not cleared or marked failed in item.json: %+v", diskEmpty)
	}
	gotEmpty, _, _ := lib.GetItem(eMeta.ItemKey)
	if len(gotEmpty.PendingParts) != 0 || gotEmpty.Status == "complete" || len(gotEmpty.Live.Incidents) != 0 {
		t.Fatalf("empty PendingParts not cleared or marked complete: %+v", gotEmpty)
	}

	dyValidDir := filepath.Join(lib.CacheDir(), "douyin", "valid-codec-backfill")
	_ = os.MkdirAll(dyValidDir, 0o700)
	initBytes, fragBytes := regressionLiveSamples(t)
	_ = os.WriteFile(filepath.Join(dyValidDir, "part-001.mp4.part"), append(append([]byte(nil), initBytes...), fragBytes...), 0o600)
	dyValidMeta, _ := lib.SaveItem(dyValidDir, library.ItemMetadata{
		Site: "douyin", Kind: "live", ID: "live:803", Status: "recording",
		PendingParts: []string{"part-001.mp4.part"},
		Live:         &library.LiveRecordingInfo{RoomIDStr: "803", Quality: 10000, QualityName: "", Codec: ""},
	})
	mgr.RecoverOrphanParts()
	gotDY, _, _ := lib.GetItem(dyValidMeta.ItemKey)
	if gotDY.Status != "complete" || gotDY.Live.Codec == "" || gotDY.Live.QualityName != "" {
		t.Fatalf("Douyin recovery should backfill Codec and skip Bilibili quality fallback: %+v", gotDY.Live)
	}

	// 3. Multi-subscription OR-merge for LiveNotify / AutoRecord + acc.Site == "".
	uids, subByUID, _ := collectBilibiliRefreshTargets([]subscription.Subscription{
		{Name: "SubA", Enabled: true, Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "8001", Name: "U8001"}}},
		{Name: "SubB", Enabled: true, LiveNotify: true, Rules: subscription.SubRules{AutoRecord: true}, Accounts: []subscription.AccountRef{{Site: "", UID: "8001", Name: "U8001"}}},
	}, nil, false)
	if len(uids) != 1 || uids[0] != 8001 || !subByUID[8001].LiveNotify || !subByUID[8001].Rules.AutoRecord {
		t.Fatalf("multi-sub OR-merge failed: uids=%v merged=%+v", uids, subByUID[8001])
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"8001": map[string]any{"uid": 8001, "room_id": 9801, "title": "Empty-Site", "uname": "U8001", "live_status": 1, "live_time": 1794024000},
		}})
	})
	var notified atomic.Int32
	mgr2, _, subMgr2, _, _ := setupLiveTestHarness(t, mux)
	mgr2.onNotify = func(n Notification) {
		if n.UID == 8001 {
			notified.Add(1)
		}
	}
	if _, err := subMgr2.Upsert(subscription.Subscription{
		Name:       "SubEmptySite",
		Enabled:    true,
		LiveNotify: true,
		Accounts:   []subscription.AccountRef{{Site: "", UID: "8001", Name: "U8001"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr2.PollNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if notified.Load() != 1 {
		t.Fatalf("empty acc.Site subscription failed to emit notification: count=%d", notified.Load())
	}
}

func TestBilibiliReconnectInnerRetryNeverUsesZeroedSelectionAndAudioStallBounded(t *testing.T) {
	// 1. Bilibili reconnect inner retry loop survives transient LiveRoomPlayInfo errors without using zeroed selection.
	initBytes, fragBytes := splitSyntheticHLSInitAndFrag(t)
	var srvURL string
	var playInfoCalls, m3u8Calls atomic.Int32
	var roomLive atomic.Bool
	roomLive.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"8101": map[string]any{"uid": 8101, "room_id": 9811, "title": "Bili Retry", "uname": "U8101", "live_status": 1, "live_time": 1794024000},
		}})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		st := 0
		if roomLive.Load() {
			st = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"room_id": 9811, "uid": 8101, "live_status": st, "live_time": 1794024000,
		}})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		call := playInfoCalls.Add(1)
		if call == 2 || call == 3 {
			w.WriteHeader(http.StatusBadGateway) // transient error during reconnect!
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"room_id": 9811, "uid": 8101, "live_status": 1,
			"playurl_info": map[string]any{"playurl": map[string]any{"stream": []map[string]any{{
				"protocol_name": "http_hls",
				"format": []map[string]any{{"format_name": "fmp4", "codec": []map[string]any{{
					"codec_name": "avc", "current_qn": 10000, "base_url": "/hls/index.m3u8",
					"url_info": []map[string]any{{"host": srvURL, "extra": ""}},
				}}}},
			}}}},
		}})
	})
	mux.HandleFunc("/hls/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		switch m3u8Calls.Add(1) {
		case 1:
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1,\n/hls/seg1.m4s\n"))
		case 2:
			w.WriteHeader(http.StatusBadGateway) // trigger reconnect
		default:
			roomLive.Store(false)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:2\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1,\n/hls/seg2.m4s\n#EXT-X-ENDLIST\n"))
		}
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fragBytes) })
	mux.HandleFunc("/hls/seg2.m4s", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fragBytes) })

	mgr, _, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL

	// Manual start must also clear any previous stoppedBroadcasts entry.
	mgr.mu.Lock()
	mgr.stoppedBroadcasts[9811] = 1794024000
	mgr.mu.Unlock()

	if _, err := mgr.StartRecording(context.Background(), 9811); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	mgr.mu.Lock()
	_, stillSuppressed := mgr.stoppedBroadcasts[9811]
	mgr.mu.Unlock()
	if stillSuppressed {
		t.Fatal("manual StartRecording did not clear stoppedBroadcasts")
	}

	done := waitUntilRoomIdle(t, mgr, "bilibili:live:9811", 6*time.Second)
	if done.RecordSegments != 2 || playInfoCalls.Load() != 4 {
		t.Fatalf("Bilibili reconnect retry failed: segments=%d playInfoCalls=%d state=%+v", done.RecordSegments, playInfoCalls.Load(), done)
	}

	// 2. Audio buffer stays bounded if video track stalls while audio keeps arriving.
	sps := mustHex(t, testAVCSPSHex)
	pps := mustHex(t, testAVCPPSHex)
	idr := mustHex(t, testAVCSliceHex)
	fb := newFLVBuilder(0x05)
	fb.writeClassicVideoConfig(0, flvVideoCodecAVC, buildAVCDecConfBytes(t, sps, pps, 4))
	fb.writeAACConfig(0, testAACConfig48kStereo)
	fb.writeClassicVideoFrame(0, flvVideoCodecAVC, true, 0, packLengthPrefixedNALUs(4, idr))
	for i := 0; i < maxPreInitAudioSamples+400; i++ {
		fb.writeAACFrame(uint32(i*20), testAACSilentFrame)
	}
	remuxer := NewFLVRemuxer(bytes.NewReader(fb.buf.Bytes()), FLVRemuxOptions{})
	partPath := filepath.Join(t.TempDir(), "stall.mp4.part")
	f, err := os.Create(partPath)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := remuxer.WritePart(context.Background(), f, nil)
	_ = f.Close()
	if err != nil || cp.BytesWritten <= 0 {
		t.Fatalf("video-stalled FLV stream failed: cp=%+v err=%v", cp, err)
	}
}

func TestFinishPartRetainsPendingPartsOnSaveErrorAndEmitsOnState(t *testing.T) {
	initBytes, fragBytes := regressionLiveSamples(t)
	mgr, lib, _ := regressionRecorder(t, http.NotFoundHandler())

	var stateUpdates atomic.Int32
	mgr.onState = func(st RoomState) {
		if st.RoomID == 42 {
			stateUpdates.Add(1)
		}
	}

	sess := regressionLiveSession(t, lib)
	mgr.mu.Lock()
	mgr.states[42] = RoomState{Site: "bilibili", RoomID: 42, Recording: true}
	mgr.sessions[42] = sess
	mgr.mu.Unlock()

	partPath := filepath.Join(sess.itemDir, "part-001.mp4.part")
	if err := os.WriteFile(partPath, append(append([]byte(nil), initBytes...), fragBytes...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.finishPart(sess, partJob{path: partPath, partRel: "part-001.mp4", pending: "part-001.mp4.part", codec: "avc"}); err != nil {
		t.Fatalf("finishPart: %v", err)
	}
	if stateUpdates.Load() == 0 {
		t.Fatal("expected finishPart to emit onState after completing part")
	}
	if len(sess.itemMeta.Media) == 0 || sess.itemMeta.Media[0].Duration < 1 {
		t.Fatalf("expected Duration >= 1 on finished part: %+v", sess.itemMeta.Media)
	}

	// Failure case: place a conflicting item.json in badDir so SaveItem rejects manifest save;
	// PendingParts must retain part-002.mp4.part and .part file must remain on disk!
	badDir := filepath.Join(lib.CacheDir(), "conflict-save-dir")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "item.json"), []byte(`{"version":1,"site":"bilibili","kind":"live","id":"live:other:999"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	badPart := filepath.Join(badDir, "part-002.mp4.part")
	if err := os.WriteFile(badPart, append(append([]byte(nil), initBytes...), fragBytes...), 0o600); err != nil {
		t.Fatal(err)
	}
	badSess := &recordSession{
		roomID:  43,
		itemDir: badDir,
		itemMeta: library.ItemMetadata{
			Site: "bilibili", Kind: "live", ID: "live:43:100", Status: "recording",
			PendingParts: []string{"part-002.mp4.part"},
			Live:         &library.LiveRecordingInfo{RoomID: 43, LiveSince: 100},
		},
	}
	if err := mgr.finishPart(badSess, partJob{path: badPart, partRel: "part-002.mp4", pending: "part-002.mp4.part", codec: "avc"}); err == nil {
		t.Fatal("expected SaveItem error for conflicting item.json")
	}
	if len(badSess.itemMeta.PendingParts) != 1 || badSess.itemMeta.PendingParts[0] != "part-002.mp4.part" {
		t.Fatalf("PendingParts was mutated before SaveItem succeeded: %+v", badSess.itemMeta.PendingParts)
	}
	if _, err := os.Stat(badPart); err != nil {
		t.Fatalf(".part file should remain on disk when SaveItem fails: %v", err)
	}
}

type blockingDouyinProvider struct {
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingDouyinProvider) SiteID() site.ID { return site.Douyin }

func (b *blockingDouyinProvider) CheckLiveStatus(ctx context.Context, uid, cachedWebRID string) (site.LiveStatus, error) {
	return site.LiveStatus{Site: site.Douyin, AccountUID: uid, WebRID: cachedWebRID, Status: site.LiveStateOffline}, nil
}

func (b *blockingDouyinProvider) SelectLiveStream(ctx context.Context, uid, roomOrWebRID string, codecOrder []string) (site.LiveStreamPlan, error) {
	b.once.Do(func() { close(b.blocked) })
	select {
	case <-b.release:
		return site.LiveStreamPlan{}, errors.New("released")
	case <-ctx.Done():
		return site.LiveStreamPlan{}, ctx.Err()
	}
}

// Concurrent StartRecording calls targeting a Bilibili canonical room ID and
// its short ID must serialize on the canonical room slot so only one caller
// runs LiveRoomPlayInfo/SaveItem and both observe the same active session.
func TestConcurrentStartRecordingCanonicalAndShortIDSerializes(t *testing.T) {
	initBytes, fragBytes := splitSyntheticHLSInitAndFrag(t)
	var srvURL string
	var playInfoHits atomic.Int32
	playEntered := make(chan struct{})
	playRelease := make(chan struct{})
	var playOnce sync.Once
	holdHLS := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     21452505,
				"short_id":    100,
				"uid":         50100,
				"live_status": 1,
				"live_time":   1794041000,
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"50100": map[string]any{
					"uid": 50100, "room_id": 21452505, "title": "短号并发测试", "uname": "主播", "live_status": 1, "live_time": 1794041000,
				},
			},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		playInfoHits.Add(1)
		playOnce.Do(func() { close(playEntered) })
		select {
		case <-playRelease:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id": 21452505, "uid": 50100, "live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{
						"stream": []map[string]any{{
							"protocol_name": "http_hls",
							"format": []map[string]any{{
								"format_name": "fmp4",
								"codec": []map[string]any{{
									"codec_name": "avc", "current_qn": 10000,
									"base_url": "/hls/short_race.m3u8",
									"url_info": []map[string]any{{"host": srvURL, "extra": ""}},
								}},
							}},
						}},
					},
				},
			},
		})
	})
	mux.HandleFunc("/hls/short_race.m3u8", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-holdHLS:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:2.0,\n/hls/seg1.m4s\n"))
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/seg1.m4s", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fragBytes) })

	mgr, _, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	mgr.freeBytesFunc = func(string) (uint64, error) { return 8 << 30, nil }
	defer close(holdHLS)

	type startRes struct {
		st  RoomState
		err error
	}
	resA := make(chan startRes, 1)
	resB := make(chan startRes, 1)

	// Caller A starts with canonical room ID 21452505 and pauses inside LiveRoomPlayInfo.
	go func() {
		st, err := mgr.StartRecording(context.Background(), 21452505)
		resA <- startRes{st, err}
	}()
	select {
	case <-playEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for canonical start to enter LiveRoomPlayInfo")
	}

	// Caller B starts concurrently with short ID 100 (resolves to canonical 21452505);
	// it must wait on bilibili:room:21452505 rather than issuing a second LiveRoomPlayInfo.
	go func() {
		st, err := mgr.StartRecording(context.Background(), 100)
		resB <- startRes{st, err}
	}()
	time.Sleep(80 * time.Millisecond)
	if got := playInfoHits.Load(); got != 1 {
		t.Fatalf("expected short-ID caller to serialize behind canonical slot (1 PlayInfo call), got %d", got)
	}

	close(playRelease)
	a := <-resA
	b := <-resB
	if a.err != nil || b.err != nil {
		t.Fatalf("concurrent StartRecording failed: a=%v b=%v", a.err, b.err)
	}
	if !a.st.Recording || !b.st.Recording || a.st.RoomID != 21452505 || b.st.RoomID != 21452505 {
		t.Fatalf("unexpected concurrent start states: a=%+v b=%+v", a.st, b.st)
	}
	if a.st.RecordItemDir != b.st.RecordItemDir {
		t.Fatalf("canonical and short start diverged: %q vs %q", a.st.RecordItemDir, b.st.RecordItemDir)
	}
	if got := playInfoHits.Load(); got != 1 {
		t.Fatalf("expected exactly 1 LiveRoomPlayInfo call across serialized starts, got %d", got)
	}
	_, _ = mgr.StopRecording(21452505)
	waitUntilRoomIdle(t, mgr, "21452505", 5*time.Second)
}

// StartRecordingByKey must route "douyin:web:<webRID>" as a webRID (not uid)
// and reject empty "douyin:web:"; StopRecordingByKey must not overwrite a
// matched Bilibili room ID with a UID scan fallback.
func TestStartRecordingByKeyHandlesDouyinWebPrefix(t *testing.T) {
	flvBytes := buildSyntheticFLVBytes(t, "avc", 30)
	mux := http.NewServeMux()
	mux.HandleFunc("/live/web_key.flv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(flvBytes)
	})
	mgr, _, _, srv, _ := setupLiveTestHarness(t, mux)
	mgr.freeBytesFunc = func(string) (uint64, error) { return 8 << 30, nil }

	var gotUID, gotRoom string
	var callMu sync.Mutex
	prov := newFakeDouyinLiveProvider()
	prov.streamFn = func(uid, roomOrWebRID string, codecOrder []string, call int) (site.LiveStreamPlan, error) {
		callMu.Lock()
		gotUID, gotRoom = uid, roomOrWebRID
		callMu.Unlock()
		return site.LiveStreamPlan{
			Site:        site.Douyin,
			AccountUID:  "sec_web_key_01",
			RoomID:      "7400000000000099881",
			WebRID:      "99881",
			Status:      site.LiveStateLive,
			Quality:     10000,
			QualityName: "原画",
			Codec:       "avc",
			Protocol:    "http_stream",
			Format:      "flv",
			Transport:   site.LiveTransportHTTPFLV,
			URLs:        []string{srv.URL + "/live/web_key.flv"},
			Media:       site.DouyinLiveTransport("Mozilla/5.0 Test", true),
		}, nil
	}
	prov.statusFn = func(uid, cachedWebRID string, call int) (site.LiveStatus, error) {
		return site.LiveStatus{
			Site:       site.Douyin,
			AccountUID: "sec_web_key_01",
			RoomID:     "7400000000000099881",
			WebRID:     "99881",
			Uname:      "WebKey主播",
			Title:      "WebKey测试",
			Status:     site.LiveStateOffline,
			LiveSince:  1794042000,
		}, nil
	}
	mgr.RegisterSiteProvider(prov)

	if _, err := mgr.StartRecordingByKey(context.Background(), "douyin:web:"); !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("expected bad_request for empty douyin:web:, got %v", err)
	}
	st, err := mgr.StartRecordingByKey(context.Background(), "douyin:web:99881")
	if err != nil {
		t.Fatalf("StartRecordingByKey(douyin:web:99881): %v", err)
	}
	callMu.Lock()
	uidSeen, roomSeen := gotUID, gotRoom
	callMu.Unlock()
	if uidSeen != "" || roomSeen != "99881" {
		t.Fatalf("douyin:web:99881 routed wrong: uid=%q roomOrWebRID=%q, want uid=\"\" roomOrWebRID=\"99881\"", uidSeen, roomSeen)
	}
	if st.RoomIDStr != "7400000000000099881" {
		t.Fatalf("unexpected room state: %+v", st)
	}
	waitUntilRoomIdle(t, mgr, "douyin:live:7400000000000099881", 5*time.Second)
}
