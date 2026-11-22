package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

type fakeWeiboSitePlanner struct {
	mu       sync.Mutex
	plans    map[string]func(call int) (site.DownloadPlan, error)
	calls    map[string]int
	authErr  error
	reported []error
}

func newFakeWeiboSitePlanner() *fakeWeiboSitePlanner {
	return &fakeWeiboSitePlanner{
		plans: make(map[string]func(call int) (site.DownloadPlan, error)),
		calls: make(map[string]int),
	}
}

func (p *fakeWeiboSitePlanner) SiteID() site.ID { return site.Weibo }

func (p *fakeWeiboSitePlanner) RequireAuth(_ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authErr
}

func (p *fakeWeiboSitePlanner) ReportAutomaticError(_ context.Context, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reported = append(p.reported, err)
}

func (p *fakeWeiboSitePlanner) setPlan(targetID string, fn func(call int) (site.DownloadPlan, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plans[targetID] = fn
}

func (p *fakeWeiboSitePlanner) callCount(targetID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[targetID]
}

func (p *fakeWeiboSitePlanner) reportedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reported)
}

func (p *fakeWeiboSitePlanner) PlanDownload(_ context.Context, targetID string, _ int) (site.DownloadPlan, error) {
	p.mu.Lock()
	p.calls[targetID]++
	call := p.calls[targetID]
	fn := p.plans[targetID]
	p.mu.Unlock()
	if fn == nil {
		return site.DownloadPlan{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindUnavailable, Message: "微博内容不存在"}
	}
	return fn(call)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// syntheticGIFMini returns a valid minimal animated GIF89a image.
func syntheticGIFMini() []byte {
	return []byte{
		'G', 'I', 'F', '8', '9', 'a',
		0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
		0x00, 0x00, 0x00, 0xFF, 0xFF, 0xFF,
		0x21, 0xF9, 0x04, 0x04, 0x0A, 0x00, 0x00, 0x00,
		0x2C, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
		0x02, 0x02, 0x44, 0x01, 0x00,
		0x3B,
	}
}

// buildSyntheticQuickTimeLivePhotoMov constructs a realistic QuickTime .mov (ftyp="qt  ",
// wide placeholder, mdat before moov, QuickTime mhlr handlers, QuickTime v1 wave-wrapped mp4a,
// and real synthetic H.264 + AAC sample payloads).
func buildSyntheticQuickTimeLivePhotoMov(t *testing.T, withAudio bool, moovAfterMdat bool) ([]byte, [][]byte) {
	t.Helper()
	baseBytes := buildProgressiveWithoutElst(t, withAudio, false, 0)
	parsed, err := mp4.DecodeFile(bytes.NewReader(baseBytes))
	if err != nil {
		t.Fatalf("DecodeFile base progressive mp4: %v", err)
	}

	// Capture every track's sample payloads before modifying container boxes so we can
	// prove bit-exact lossless preservation after repackaging.
	origSamples := extractTrackSamplePayloads(t, baseBytes, parsed)

	// Convert moov tracks into QuickTime .mov dialect (mhlr handlers + v1 wave-wrapped mp4a).
	for _, trak := range parsed.Moov.Traks {
		if trak == nil || trak.Mdia == nil {
			continue
		}
		if hdlr := trak.Mdia.Hdlr; hdlr != nil {
			// QuickTime sets PreDefined="mhlr" (0x6d686c72) and Pascal-length component name.
			hdlr.PreDefined = 0x6d686c72
			hdlr.Name = "\x12Core Media Handler"
			hdlr.LacksNullTermination = true
		}
		if trak.Mdia.Minf != nil && trak.Mdia.Minf.Stbl != nil && trak.Mdia.Minf.Stbl.Stsd != nil {
			for _, ch := range trak.Mdia.Minf.Stbl.Stsd.Children {
				if ase, ok := ch.(*mp4.AudioSampleEntryBox); ok && ase.Esds != nil {
					esds := ase.Esds
					wave := &mp4.WaveBox{}
					wave.AddChild(&mp4.FrmaBox{DataFormat: "mp4a"})
					wave.AddChild(esds)
					// Remove top-level esds and attach wave box with QuickTime v1 extension.
					var filtered []mp4.Box
					for _, sub := range ase.Children {
						if sub != esds {
							filtered = append(filtered, sub)
						}
					}
					ase.Children = filtered
					ase.Esds = nil
					ase.QuickTimeVersion = 1
					ase.QuickTimeV1 = &mp4.QuickTimeV1SoundDescription{
						SamplesPerPacket: 1024,
						BytesPerSample:   2,
					}
					ase.AddChild(wave)
				}
			}
		}
	}

	qtFtyp := mp4.NewFtyp("qt  ", 0x00000200, []string{"qt  "})
	wideBox := mp4.CreateUnknownBox("wide", 8, nil)

	oldMdatPayload := parsed.Mdat.PayloadAbsoluteOffset()
	var boxes []mp4.Box
	if moovAfterMdat {
		boxes = []mp4.Box{qtFtyp, wideBox, parsed.Mdat, parsed.Moov}
	} else {
		boxes = []mp4.Box{qtFtyp, wideBox, parsed.Moov, parsed.Mdat}
	}

	var pos uint64
	var newMdatPayload uint64
	for _, b := range boxes {
		if mdat, ok := b.(*mp4.MdatBox); ok {
			newMdatPayload = pos + mdat.HeaderSize()
		}
		pos += b.Size()
	}
	delta := int64(newMdatPayload) - int64(oldMdatPayload)
	for _, trak := range parsed.Moov.Traks {
		stbl := trak.Mdia.Minf.Stbl
		if stbl.Stco != nil {
			for i := range stbl.Stco.ChunkOffset {
				stbl.Stco.ChunkOffset[i] = uint32(int64(stbl.Stco.ChunkOffset[i]) + delta)
			}
		}
		if stbl.Co64 != nil {
			for i := range stbl.Co64.ChunkOffset {
				stbl.Co64.ChunkOffset[i] = uint64(int64(stbl.Co64.ChunkOffset[i]) + delta)
			}
		}
	}

	var out bytes.Buffer
	for _, b := range boxes {
		if err := b.Encode(&out); err != nil {
			t.Fatalf("encode qt box %s: %v", b.Type(), err)
		}
	}
	return out.Bytes(), origSamples
}

func extractTrackSamplePayloads(t *testing.T, raw []byte, parsed *mp4.File) [][]byte {
	t.Helper()
	var payloads [][]byte
	for _, trak := range parsed.Moov.Traks {
		stbl := trak.Mdia.Minf.Stbl
		var offsets []uint64
		if stbl.Stco != nil {
			for _, o := range stbl.Stco.ChunkOffset {
				offsets = append(offsets, uint64(o))
			}
		} else if stbl.Co64 != nil {
			offsets = append(offsets, stbl.Co64.ChunkOffset...)
		}
		sampleIdx := 0
		for chunkIdx, chunkOff := range offsets {
			chunkNum := uint32(chunkIdx + 1)
			spc := samplesInChunk(stbl.Stsc, chunkNum)
			cur := chunkOff
			for s := uint32(0); s < spc && sampleIdx < int(stbl.Stsz.SampleNumber); s++ {
				sz := uint64(stbl.Stsz.GetSampleSize(sampleIdx + 1))
				if cur+sz > uint64(len(raw)) {
					t.Fatalf("sample %d out of bounds: off=%d sz=%d len=%d", sampleIdx, cur, sz, len(raw))
				}
				cp := make([]byte, sz)
				copy(cp, raw[cur:cur+sz])
				payloads = append(payloads, cp)
				cur += sz
				sampleIdx++
			}
		}
	}
	return payloads
}

func samplesInChunk(stsc *mp4.StscBox, chunkNum uint32) uint32 {
	if stsc == nil || len(stsc.Entries) == 0 {
		return 0
	}
	var spc uint32
	for _, e := range stsc.Entries {
		if e.FirstChunk <= chunkNum {
			spc = e.SamplesPerChunk
		} else {
			break
		}
	}
	return spc
}

func TestLivePhotoQuickTimeMovLosslessRepackaging(t *testing.T) {
	t.Run("repackages QuickTime qt mov with wide mdat-first mhlr and v1 wave mp4a losslessly", func(t *testing.T) {
		movBytes, origSamples := buildSyntheticQuickTimeLivePhotoMov(t, true, true)
		if len(origSamples) == 0 {
			t.Fatal("expected synthetic QuickTime .mov samples")
		}
		p := filepath.Join(t.TempDir(), "livephoto.mov")
		if err := os.WriteFile(p, movBytes, 0o600); err != nil {
			t.Fatalf("write mov: %v", err)
		}

		summary, repackaged, err := EnsureLivePhotoMP4Context(context.Background(), p, 2.0)
		if err != nil {
			t.Fatalf("EnsureLivePhotoMP4Context: %v", err)
		}
		if !repackaged {
			t.Fatal("expected QuickTime .mov to be losslessly repackaged")
		}
		if !summary.MoovBeforeMdat {
			t.Fatal("expected moov before mdat after repackaging")
		}
		if summary.Width <= 0 || summary.Height <= 0 || summary.DurationSec <= 0 {
			t.Fatalf("unexpected summary dimensions/duration: %+v", summary)
		}

		repackagedBytes, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read repackaged mp4: %v", err)
		}
		parsed, err := mp4.DecodeFile(bytes.NewReader(repackagedBytes))
		if err != nil {
			t.Fatalf("DecodeFile repackaged mp4: %v", err)
		}
		if parsed.Ftyp == nil || parsed.Ftyp.MajorBrand() != "mp42" {
			t.Fatalf("ftyp major brand = %v, want mp42", parsed.Ftyp)
		}
		for _, ch := range parsed.Children {
			if ch.Type() == "wide" {
				t.Fatal("expected QuickTime wide box to be dropped")
			}
		}
		if needsQuickTimeRepackage(parsed) {
			t.Fatal("repackaged mp4 should no longer require QuickTime repackaging")
		}
		for _, trak := range parsed.Moov.Traks {
			if trak.Mdia.Hdlr.QuickTimeComponentType() != "" || trak.Mdia.Hdlr.PreDefined != 0 {
				t.Fatalf("expected normalized hdlr, got preDefined=%d qtType=%q", trak.Mdia.Hdlr.PreDefined, trak.Mdia.Hdlr.QuickTimeComponentType())
			}
			for _, ch := range trak.Mdia.Minf.Stbl.Stsd.Children {
				if ase, ok := ch.(*mp4.AudioSampleEntryBox); ok {
					if ase.QuickTimeVersion != 0 || ase.Wave != nil || ase.Esds == nil {
						t.Fatalf("expected normalized ISO mp4a with hoisted esds, got qtVer=%d wave=%v esds=%v", ase.QuickTimeVersion, ase.Wave != nil, ase.Esds != nil)
					}
				}
			}
		}

		afterSamples := extractTrackSamplePayloads(t, repackagedBytes, parsed)
		if len(afterSamples) != len(origSamples) {
			t.Fatalf("sample count changed: got %d, want %d", len(afterSamples), len(origSamples))
		}
		for i := range origSamples {
			if !bytes.Equal(afterSamples[i], origSamples[i]) {
				t.Fatalf("sample %d payload changed during lossless repackaging", i)
			}
		}

		// Standard EnsureSingleMP4 must also accept the repackaged file cleanly.
		if _, err := EnsureSingleMP4(p, 2.0); err != nil {
			t.Fatalf("EnsureSingleMP4 on repackaged clip: %v", err)
		}
	})

	t.Run("repackages QuickTime qt mov when moov is already before mdat", func(t *testing.T) {
		movBytes, origSamples := buildSyntheticQuickTimeLivePhotoMov(t, true, false)
		p := filepath.Join(t.TempDir(), "livephoto_moov_first.mov")
		if err := os.WriteFile(p, movBytes, 0o600); err != nil {
			t.Fatalf("write mov: %v", err)
		}
		summary, repackaged, err := EnsureLivePhotoMP4Context(context.Background(), p, 2.0)
		if err != nil {
			t.Fatalf("EnsureLivePhotoMP4Context: %v", err)
		}
		if !repackaged || !summary.MoovBeforeMdat {
			t.Fatalf("expected repackaged moov-first mp4, got repackaged=%v summary=%+v", repackaged, summary)
		}
		outBytes, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read repackaged: %v", err)
		}
		parsed, err := mp4.DecodeFile(bytes.NewReader(outBytes))
		if err != nil {
			t.Fatalf("decode repackaged: %v", err)
		}
		afterSamples := extractTrackSamplePayloads(t, outBytes, parsed)
		if len(afterSamples) != len(origSamples) {
			t.Fatalf("sample count mismatch: got %d want %d", len(afterSamples), len(origSamples))
		}
		for i := range origSamples {
			if !bytes.Equal(afterSamples[i], origSamples[i]) {
				t.Fatalf("sample %d payload mismatch", i)
			}
		}
	})

	t.Run("standard moov-first ISO MP4 fast path skips repackaging", func(t *testing.T) {
		raw := buildProgressiveWithoutElst(t, true, false, 0)
		p := filepath.Join(t.TempDir(), "standard.mp4")
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatalf("write mp4: %v", err)
		}
		origSum := sha256.Sum256(raw)

		summary, repackaged, err := EnsureLivePhotoMP4Context(context.Background(), p, 2.0)
		if err != nil {
			t.Fatalf("EnsureLivePhotoMP4Context: %v", err)
		}
		if repackaged {
			t.Fatal("expected standard ISO MP4 to skip repackaging")
		}
		if summary.SHA256 != hex.EncodeToString(origSum[:]) {
			t.Fatalf("expected file untouched on fast path")
		}
	})

	t.Run("rejects encrypted and audio-only QuickTime mov", func(t *testing.T) {
		movBytes, _ := buildSyntheticQuickTimeLivePhotoMov(t, true, true)
		parsed, err := mp4.DecodeFile(bytes.NewReader(movBytes))
		if err != nil {
			t.Fatalf("decode mov: %v", err)
		}
		// Inject CENC encv into video track stsd.
		for _, trak := range parsed.Moov.Traks {
			if trak.Mdia.Hdlr.HandlerType == "vide" {
				stsd := trak.Mdia.Minf.Stbl.Stsd
				encv := mp4.CreateVisualSampleEntryBox("encv", 640, 360, nil)
				stsd.Children = []mp4.Box{encv}
				stsd.SampleCount = 1
			}
		}
		var encBuf bytes.Buffer
		if err := parsed.Encode(&encBuf); err != nil {
			t.Fatalf("encode encv mov: %v", err)
		}
		encPath := filepath.Join(t.TempDir(), "enc.mov")
		if err := os.WriteFile(encPath, encBuf.Bytes(), 0o600); err != nil {
			t.Fatalf("write enc.mov: %v", err)
		}
		if _, _, err := EnsureLivePhotoMP4Context(context.Background(), encPath, 2.0); !IsEncryptedMedia(err) {
			t.Fatalf("expected EncryptedMediaError, got %v", err)
		}

		// Audio-only QuickTime mov must be rejected.
		parsed2, err := mp4.DecodeFile(bytes.NewReader(movBytes))
		if err != nil {
			t.Fatalf("decode mov2: %v", err)
		}
		var onlyAudio []mp4.Box
		var audioTraks []*mp4.TrakBox
		for _, ch := range parsed2.Moov.Children {
			if tr, ok := ch.(*mp4.TrakBox); ok && tr.Mdia.Hdlr.HandlerType == "vide" {
				continue
			}
			onlyAudio = append(onlyAudio, ch)
			if tr, ok := ch.(*mp4.TrakBox); ok {
				audioTraks = append(audioTraks, tr)
			}
		}
		parsed2.Moov.Children = onlyAudio
		parsed2.Moov.Traks = audioTraks
		var audioBuf bytes.Buffer
		if err := parsed2.Encode(&audioBuf); err != nil {
			t.Fatalf("encode audio-only mov: %v", err)
		}
		audioPath := filepath.Join(t.TempDir(), "audio_only.mov")
		if err := os.WriteFile(audioPath, audioBuf.Bytes(), 0o600); err != nil {
			t.Fatalf("write audio_only.mov: %v", err)
		}
		if _, _, err := EnsureLivePhotoMP4Context(context.Background(), audioPath, 2.0); !IsSingleMP4ValidationError(err) {
			t.Fatalf("expected SingleMP4ValidationError for audio-only mov, got %v", err)
		}
	})
}

func TestWeiboSingleVideoAndStandaloneVideoExpiryRangeResumeAndZeroSignedLeak(t *testing.T) {
	mp4Payload := buildProgressiveWithoutElst(t, true, true, 12*1024) // mdat-before-moov, >12 KiB for multi-range chunks
	jpegPayload := syntheticJPEGMini()

	const (
		expiredSig = "ExpiredSigSentinel0001"
		freshSig   = "FreshSigSentinel0002"
		standalone = "StandaloneSigSentinel0003"
		cookieVal  = "SecretWeiboSubSentinel99"
	)

	var (
		mu             sync.Mutex
		seenCookies    []string
		seenReferers   []string
		freshRangeHdrs []string
		expiredHits    atomic.Int32
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if c := r.Header.Get("Cookie"); c != "" {
			seenCookies = append(seenCookies, c)
		}
		seenReferers = append(seenReferers, r.Header.Get("Referer"))
		mu.Unlock()

		host := strings.ToLower(r.Host)
		if host == "" {
			host = strings.ToLower(r.URL.Host)
		}
		switch {
		case host == "wx1.sinaimg.cn" && r.URL.Path == "/orj360/cover01.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(jpegPayload)))
			_, _ = w.Write(jpegPayload)

		case host == "f.video.weibocdn.com" && r.URL.Path == "/o0/post_video_expired.mp4":
			hit := expiredHits.Add(1)
			if hit == 1 {
				// First segment succeeds (bytes 0-4095), then subsequent range hits 403 expiry.
				serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)
				return
			}
			w.WriteHeader(http.StatusForbidden)

		case host == "f.video.weibocdn.com" && r.URL.Path == "/o0/post_video_fresh.mp4":
			mu.Lock()
			freshRangeHdrs = append(freshRangeHdrs, r.Header.Get("Range"))
			mu.Unlock()
			serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)

		case host == "f.video.weibocdn.com" && r.URL.Path == "/o0/standalone_video.mp4":
			serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)

		case host == "f.video.weibocdn.com" && r.URL.Path == "/o0/always_403.mp4":
			w.WriteHeader(http.StatusForbidden)

		case host == "f.video.weibocdn.com" && r.URL.Path == "/o0/disguised_html.mp4":
			body := []byte("<!DOCTYPE html><html><body>Access Denied</body></html>")
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)

		case host == "f.video.weibocdn.com" && r.URL.Path == "/o0/disguised_json.mp4":
			body := []byte(`{"status":403,"message":"expired","ok":0,"msg":"token expired"}`)
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)

		case host == "f.video.weibocdn.com" && r.URL.Path == "/o0/redirect_evil.mp4":
			http.Redirect(w, r, "https://evil.example/hijack.mp4", http.StatusFound)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mgr, libStore, _, dbPath, eventBuf := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
	mgr.SetChunkSize(4096)
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			resp.Request = req
			return resp, nil
		})
	})

	planner := newFakeWeiboSitePlanner()
	mgr.RegisterSitePlanner(planner)

	strictTransport := site.WeiboMediaTransport("TRACE-Test-UA", false)

	// 1. Single-video Weibo post -> weibo:dynamic:<mid>, 403 mid-file expiry -> refresh once -> Range resume.
	const postMid = "5000000000000001"
	planner.setPlan(postMid, func(call int) (site.DownloadPlan, error) {
		videoURL := "https://f.video.weibocdn.com/o0/post_video_expired.mp4?ssig=" + expiredSig + "&Expires=1763700000&KID=unistore,video"
		if call >= 2 {
			videoURL = "https://f.video.weibocdn.com/o0/post_video_fresh.mp4?ssig=" + freshSig + "&Expires=1763703600&KID=unistore,video"
		}
		return site.DownloadPlan{
			Site:         site.Weibo,
			ItemKey:      "weibo:dynamic:" + postMid,
			TargetID:     postMid,
			Kind:         "single_mp4",
			CanonicalURL: "https://weibo.com/1000000001/Q00000001?ssig=" + expiredSig,
			AuthorUID:    "001000000001", // verifies leading-zero stripping
			AuthorName:   "合成博主",
			AuthorFace:   "https://wx1.sinaimg.cn/orj360/avatar.jpg?SUB=" + cookieVal,
			Title:        "单视频微博测试",
			Desc:         "单视频微博完整正文",
			PubTs:        1763700000,
			DurationSec:  2,
			VideoCodec:   "avc",
			QualityLabel: "1080P",
			Width:        1920,
			Height:       1080,
			VideoURLs:    []string{videoURL},
			CoverURLs:    []string{"https://wx1.sinaimg.cn/orj360/cover01.jpg?ssig=" + expiredSig + "&Expires=1763700000&KID=unistore,video"},
			Media:        strictTransport,
		}, nil
	})

	task1, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: postMid})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork single-video post: %v", err)
	}
	done1 := waitForTaskTerminal(t, mgr, task1.TaskID, 10*time.Second)
	if done1.Status != "completed" {
		t.Fatalf("single-video post status=%s err=%s", done1.Status, done1.ErrorMessage)
	}
	if planner.callCount(postMid) != 2 {
		t.Fatalf("expected 1 initial plan + 1 refresh on 403, got %d calls", planner.callCount(postMid))
	}

	mu.Lock()
	if len(seenCookies) > 0 {
		t.Fatalf("CDN requests must never send Cookie, saw: %v", seenCookies)
	}
	if len(seenReferers) == 0 {
		t.Fatal("expected CDN requests")
	}
	for _, ref := range seenReferers {
		if ref != "https://weibo.com/" {
			t.Fatalf("CDN Referer = %q, want https://weibo.com/", ref)
		}
	}
	if len(freshRangeHdrs) == 0 || !strings.HasPrefix(freshRangeHdrs[0], "bytes=4096-") {
		t.Fatalf("expected Range resume from byte 4096 after 403 refresh, got %v", freshRangeHdrs)
	}
	mu.Unlock()

	meta1, exists, err := libStore.GetItem("weibo:dynamic:" + postMid)
	if err != nil || !exists {
		t.Fatalf("GetItem(weibo:dynamic:%s): exists=%v err=%v", postMid, exists, err)
	}
	wantDir1 := filepath.Join(libStore.CacheDir(), "weibo", "1000000001", "posts", postMid)
	if filepath.Clean(meta1.ItemDir) != filepath.Clean(wantDir1) {
		t.Fatalf("ItemDir = %q, want %q", meta1.ItemDir, wantDir1)
	}
	if meta1.Kind != "dynamic" || meta1.Status != "complete" || meta1.Author.UID != "1000000001" {
		t.Fatalf("unexpected meta1: kind=%q status=%q uid=%q", meta1.Kind, meta1.Status, meta1.Author.UID)
	}
	if meta1.DouyinVideo == nil || meta1.DouyinVideo.File != "video.mp4" {
		t.Fatalf("expected DouyinVideo metadata for video.mp4, got %+v", meta1.DouyinVideo)
	}
	summary1, err := InspectMP4File(filepath.Join(meta1.ItemDir, "video.mp4"))
	if err != nil || !summary1.MoovBeforeMdat {
		t.Fatalf("expected relocated moov-first video.mp4, err=%v summary=%+v", err, summary1)
	}
	if _, err := os.Stat(filepath.Join(meta1.ItemDir, "cover.jpg")); err != nil {
		t.Fatalf("expected cover.jpg: %v", err)
	}

	// 2. Standalone 1034: video with no host post -> weibo:video:1034_<id>.
	const standaloneOID = "1034:5000000000000002"
	planner.setPlan(standaloneOID, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:         site.Weibo,
			ItemKey:      "weibo:video:1034_5000000000000002",
			TargetID:     standaloneOID,
			Kind:         "single_mp4",
			CanonicalURL: "https://weibo.com/tv/show/" + standaloneOID,
			AuthorUID:    "1000000001",
			AuthorName:   "合成博主",
			Title:        "独立视频对象",
			DurationSec:  2,
			VideoCodec:   "avc",
			QualityLabel: "720P",
			VideoURLs:    []string{"https://f.video.weibocdn.com/o0/standalone_video.mp4?ssig=" + standalone + "&Expires=1763703600&KID=unistore,video"},
			Media:        strictTransport,
		}, nil
	})
	task2, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: standaloneOID})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork standalone video: %v", err)
	}
	done2 := waitForTaskTerminal(t, mgr, task2.TaskID, 10*time.Second)
	if done2.Status != "completed" {
		t.Fatalf("standalone video status=%s err=%s", done2.Status, done2.ErrorMessage)
	}
	meta2, exists, err := libStore.GetItem("weibo:video:1034_5000000000000002")
	if err != nil || !exists {
		t.Fatalf("GetItem standalone video: exists=%v err=%v", exists, err)
	}
	wantDir2 := filepath.Join(libStore.CacheDir(), "weibo", "1000000001", "posts", "1034_5000000000000002")
	if filepath.Clean(meta2.ItemDir) != filepath.Clean(wantDir2) {
		t.Fatalf("standalone ItemDir = %q, want %q", meta2.ItemDir, wantDir2)
	}
	if meta2.Kind != "video" || meta2.URL != "https://weibo.com/tv/show/1034:5000000000000002" {
		t.Fatalf("unexpected standalone meta: kind=%q url=%q", meta2.Kind, meta2.URL)
	}

	// 3. Persistent 403 refreshes at most ONCE and fails cleanly.
	const mid403 = "5000000000000003"
	planner.setPlan(mid403, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:        site.Weibo,
			ItemKey:     "weibo:dynamic:" + mid403,
			TargetID:    mid403,
			Kind:        "single_mp4",
			AuthorUID:   "1000000001",
			AuthorName:  "合成博主",
			Title:       "持续403测试",
			DurationSec: 2,
			VideoURLs:   []string{"https://f.video.weibocdn.com/o0/always_403.mp4?ssig=" + expiredSig},
			Media:       strictTransport,
		}, nil
	})
	t403, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: mid403})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork 403: %v", err)
	}
	done403 := waitForTaskTerminal(t, mgr, t403.TaskID, 10*time.Second)
	if done403.Status != "failed" {
		t.Fatalf("persistent 403 status=%s, want failed", done403.Status)
	}
	if planner.callCount(mid403) != 2 {
		t.Fatalf("persistent 403 planner calls = %d, want 2 (1 initial + at most 1 refresh)", planner.callCount(mid403))
	}

	// 4. Reject disguised HTML, disguised JSON, non-whitelist host, non-whitelist redirect,
	// video.weibo.com/media/play on regular video, and duration mismatch (> max(2s, 3%)).
	rejectCases := []struct {
		name        string
		mid         string
		videoURL    string
		durationSec int
	}{
		{"disguised HTML", "5000000000000004", "https://f.video.weibocdn.com/o0/disguised_html.mp4", 2},
		{"disguised JSON", "5000000000000005", "https://f.video.weibocdn.com/o0/disguised_json.mp4", 2},
		{"non-whitelist host", "5000000000000006", "https://evil.example/video.mp4", 2},
		{"non-whitelist redirect", "5000000000000007", "https://f.video.weibocdn.com/o0/redirect_evil.mp4", 2},
		{"livephoto redirector rejected on regular video", "5000000000000008", "https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001.mov", 2},
		{"duration mismatch", "5000000000000009", "https://f.video.weibocdn.com/o0/standalone_video.mp4", 120},
	}
	for _, tc := range rejectCases {
		tc := tc
		planner.setPlan(tc.mid, func(_ int) (site.DownloadPlan, error) {
			return site.DownloadPlan{
				Site:        site.Weibo,
				ItemKey:     "weibo:dynamic:" + tc.mid,
				TargetID:    tc.mid,
				Kind:        "single_mp4",
				AuthorUID:   "1000000001",
				AuthorName:  "合成博主",
				Title:       tc.name,
				DurationSec: tc.durationSec,
				VideoURLs:   []string{tc.videoURL},
				// Intentionally pass LivePhoto transport + PublicRedirects=true to verify
				// normalizeWeiboMediaTransport strips both for regular single videos.
				Media: func() site.MediaTransportSpec {
					sp := site.WeiboLivePhotoMediaTransport("TRACE-Test-UA", false)
					sp.PublicRedirects = true
					return sp
				}(),
			}, nil
		})
		tk, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: tc.mid})
		if err != nil {
			t.Fatalf("enqueue %s: %v", tc.name, err)
		}
		done := waitForTaskTerminal(t, mgr, tk.TaskID, 10*time.Second)
		if done.Status != "failed" {
			t.Fatalf("%s: status=%s, want failed", tc.name, done.Status)
		}
	}

	// 5. Verify ZERO signed URL sentinels or cookie markers leaked into item.json, library.db, or task logs.
	assertNoSentinelsInTreeAndDB(
		t,
		libStore.CacheDir(),
		dbPath,
		eventBuf.String(),
		[]string{expiredSig, freshSig, standalone, cookieVal, "ssig=", "Expires=", "KID=", "post_video_expired.mp4", "post_video_fresh.mp4"},
		[]string{"weibocdn.com", "video.weibo.com"},
	)
}

func TestWeiboMultiImageGifLivePhotoMixedMediaPartialAndForwards(t *testing.T) {
	jpegBytes := syntheticJPEGMini()
	pngBytes := syntheticPNGMini()
	webpBytes := syntheticWEBPMini()
	gifBytes := syntheticGIFMini()
	movBytes, origLiveSamples := buildSyntheticQuickTimeLivePhotoMov(t, true, true)
	mixedMP4Bytes := buildProgressiveWithoutElst(t, true, true, 0)

	const (
		liveSig = "LivePhotoSigSentinel0004"
		mixSig  = "MixedVideoSigSentinel0005"
	)

	var (
		liveInitialHits atomic.Int32
		liveCDNHits     atomic.Int32
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if host == "" {
			host = strings.ToLower(r.URL.Host)
		}
		switch {
		case host == "wx1.sinaimg.cn" && strings.HasPrefix(r.URL.Path, "/large/pic_"):
			// Serve extension-less URLs with different magic headers to verify magic sniffing.
			switch {
			case strings.HasSuffix(r.URL.Path, "_png"):
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", strconv.Itoa(len(pngBytes)))
				_, _ = w.Write(pngBytes)
			case strings.HasSuffix(r.URL.Path, "_webp"):
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", strconv.Itoa(len(webpBytes)))
				_, _ = w.Write(webpBytes)
			case strings.HasSuffix(r.URL.Path, "_gif"):
				w.Header().Set("Content-Type", "image/gif")
				w.Header().Set("Content-Length", strconv.Itoa(len(gifBytes)))
				_, _ = w.Write(gifBytes)
			case strings.HasSuffix(r.URL.Path, "_missing"):
				w.WriteHeader(http.StatusNotFound)
			default:
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set("Content-Length", strconv.Itoa(len(jpegBytes)))
				_, _ = w.Write(jpegBytes)
			}

		case host == "video.weibo.com" && r.URL.Path == "/media/play":
			liveInitialHits.Add(1)
			mode := r.URL.Query().Get("mode")
			switch mode {
			case "loop":
				// Second hop back to video.weibo.com/media/play must be blocked by ValidateRedirectURL!
				http.Redirect(w, r, "https://video.weibo.com/media/play?mode=final", http.StatusFound)
			case "evil":
				// Redirect off CDN allowlist must be blocked!
				http.Redirect(w, r, "https://evil.example/live.mov", http.StatusFound)
			default:
				http.Redirect(w, r, "https://livephoto.us.sinaimg.cn/001live.mov?Expires=1763703600&ssig="+liveSig+"&KID=unistore,video", http.StatusFound)
			}

		case host == "livephoto.us.sinaimg.cn" && r.URL.Path == "/001live.mov":
			liveCDNHits.Add(1)
			serveRangeBytes(w, r, movBytes, "video/quicktime", 0, nil)

		case host == "f.video.weibocdn.com" && r.URL.Path == "/o0/mixed_10.mp4":
			serveRangeBytes(w, r, mixedMP4Bytes, "video/mp4", 0, nil)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mgr, libStore, _, dbPath, eventBuf := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			resp.Request = req
			return resp, nil
		})
	})
	planner := newFakeWeiboSitePlanner()
	mgr.RegisterSitePlanner(planner)

	strictTransport := site.WeiboMediaTransport("TRACE-Test-UA", false)

	// Subtest A: >9 images (11 ordered items) + GIF + LivePhoto (.mov -> .mp4) + mixed-media video + long text.
	const richMid = "5000000000000020"
	longText := strings.Repeat("这是一条包含长文、超过九图、动图、实况和混排视频的合成微博正文。", 20)
	livePlayURL := "https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001live.mov%3FExpires%3D1763703600%26ssig%3D" + liveSig + "%26KID%3Dunistore%2Cvideo"
	assets := []site.MediaImageAsset{
		{Index: 1, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_01_jpg"}},
		{Index: 2, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_02_png"}},
		{Index: 3, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_03_webp"}},
		{Index: 4, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_04_jpg"}},
		{Index: 5, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_05_jpg"}},
		{Index: 6, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_06_jpg"}},
		{Index: 7, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_07_jpg"}},
		{Index: 8, MediaRole: "gif", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_08_gif"}},
		{Index: 9, MediaRole: "live_photo", DurationSec: 2, ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_09_jpg"}, VideoURLs: []string{livePlayURL}},
		{Index: 10, MediaRole: "video", DurationSec: 2, VideoCodec: "avc", QualityLabel: "720P", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_10_jpg"}, VideoURLs: []string{"https://f.video.weibocdn.com/o0/mixed_10.mp4?ssig=" + mixSig + "&Expires=1763703600&KID=unistore,video"}},
		{Index: 11, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_11_jpg"}},
	}
	planner.setPlan(richMid, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:         site.Weibo,
			ItemKey:      "weibo:dynamic:" + richMid,
			TargetID:     richMid,
			Kind:         "image_note",
			CanonicalURL: "https://weibo.com/detail/" + richMid,
			AuthorUID:    "1000000001",
			AuthorName:   "合成博主",
			Title:        "十一项混排长文微博",
			Desc:         longText,
			PubTs:        1763700100,
			Images:       assets,
			Media:        site.WeiboLivePhotoMediaTransport("TRACE-Test-UA", false),
		}, nil
	})

	tkRich, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: richMid})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork rich post: %v", err)
	}
	doneRich := waitForTaskTerminal(t, mgr, tkRich.TaskID, 15*time.Second)
	if doneRich.Status != "completed" {
		t.Fatalf("rich post status=%s err=%s", doneRich.Status, doneRich.ErrorMessage)
	}
	if liveInitialHits.Load() == 0 || liveCDNHits.Load() == 0 {
		t.Fatalf("expected LivePhoto initial hop + CDN redirect hit, got init=%d cdn=%d", liveInitialHits.Load(), liveCDNHits.Load())
	}

	metaRich, exists, err := libStore.GetItem("weibo:dynamic:" + richMid)
	if err != nil || !exists {
		t.Fatalf("GetItem rich post: exists=%v err=%v", exists, err)
	}
	if metaRich.Text != longText || metaRich.Status != "complete" {
		t.Fatalf("rich post text/status mismatch: status=%q len(text)=%d want=%d", metaRich.Status, len(metaRich.Text), len(longText))
	}
	if len(metaRich.NotePages) != 11 {
		t.Fatalf("expected 11 ordered notePages, got %d", len(metaRich.NotePages))
	}
	expectedPaths := []string{
		"images/01.jpg",
		"images/02.png",
		"images/03.webp",
		"images/08.gif",
		"images/09.jpg",
		"images/09_live.mp4",
		"images/10.jpg",
		"images/10.mp4",
		"images/11.jpg",
	}
	for _, rel := range expectedPaths {
		if _, err := os.Stat(filepath.Join(metaRich.ItemDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("expected cached file %s: %v", rel, err)
		}
	}
	// Verify GIF bytes preserved intact.
	savedGif, err := os.ReadFile(filepath.Join(metaRich.ItemDir, "images", "08.gif"))
	if err != nil || !bytes.Equal(savedGif, gifBytes) {
		t.Fatalf("GIF file not preserved verbatim: err=%v len=%d", err, len(savedGif))
	}
	// Verify LivePhoto motion clip is repackaged ISO MP4 with identical sample payloads.
	savedLive, err := os.ReadFile(filepath.Join(metaRich.ItemDir, "images", "09_live.mp4"))
	if err != nil {
		t.Fatalf("read 09_live.mp4: %v", err)
	}
	parsedLive, err := mp4.DecodeFile(bytes.NewReader(savedLive))
	if err != nil || parsedLive.Ftyp == nil || parsedLive.Ftyp.MajorBrand() != "mp42" {
		t.Fatalf("expected repackaged mp42 LivePhoto clip, err=%v ftyp=%v", err, parsedLive.Ftyp)
	}
	if gotSamples := extractTrackSamplePayloads(t, savedLive, parsedLive); len(gotSamples) != len(origLiveSamples) {
		t.Fatalf("LivePhoto sample count = %d, want %d", len(gotSamples), len(origLiveSamples))
	}
	if metaRich.NotePages[8].Kind != "motion" || metaRich.NotePages[8].Image != "images/09.jpg" || metaRich.NotePages[8].Motion != "images/09_live.mp4" {
		t.Fatalf("unexpected page 9 LivePhoto record: %+v", metaRich.NotePages[8])
	}
	if metaRich.NotePages[9].Kind != "video" || metaRich.NotePages[9].Video != "images/10.mp4" || metaRich.NotePages[9].Cover != "images/10.jpg" {
		t.Fatalf("unexpected page 10 mixed video record: %+v", metaRich.NotePages[9])
	}

	// Subtest B: LivePhoto redirect back to video.weibo.com/media/play OR to evil.example is blocked,
	// and single missing attachment marks yellow dot (partial) without failing remaining images.
	const partialMid = "5000000000000021"
	planner.setPlan(partialMid, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.Weibo,
			ItemKey:    "weibo:dynamic:" + partialMid,
			TargetID:   partialMid,
			Kind:       "image_note",
			AuthorUID:  "1000000001",
			AuthorName: "合成博主",
			Title:      "单附件缺失黄点测试",
			PubTs:      1763700200,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_01_jpg"}},
				{Index: 2, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_02_missing"}},
				{Index: 3, MediaRole: "live_photo", DurationSec: 2, ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_03_jpg"}, VideoURLs: []string{"https://video.weibo.com/media/play?mode=loop"}},
			},
			Media: strictTransport,
		}, nil
	})
	tkPartial, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: partialMid})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork partial post: %v", err)
	}
	donePartial := waitForTaskTerminal(t, mgr, tkPartial.TaskID, 10*time.Second)
	if donePartial.Status != "partial" {
		t.Fatalf("partial post task status = %s, want partial (err=%s)", donePartial.Status, donePartial.ErrorMessage)
	}
	metaPartial, exists, err := libStore.GetItem("weibo:dynamic:" + partialMid)
	if err != nil || !exists {
		t.Fatalf("GetItem partial post: exists=%v err=%v", exists, err)
	}
	if metaPartial.Status != "partial" || len(metaPartial.Missing) != 2 {
		t.Fatalf("expected partial status with 2 missing records (404 img + blocked livephoto loop), got status=%q missing=%+v", metaPartial.Status, metaPartial.Missing)
	}
	if dot := library.ComputeDotState(metaPartial); dot.Dot != "yellow" {
		t.Fatalf("expected yellow dot for partial item, got %+v", dot)
	}
	if _, err := os.Stat(filepath.Join(metaPartial.ItemDir, "images", "01.jpg")); err != nil {
		t.Fatalf("expected non-missing image 01.jpg to still be saved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(metaPartial.ItemDir, "images", "03.jpg")); err != nil {
		t.Fatalf("expected LivePhoto still 03.jpg to still be saved when motion redirect is blocked: %v", err)
	}

	// Subtest C: Forwarded post stored in weibo/<forwarder>/forwards/<mid>/ with via,
	// original author directory untouched, and re-caching never moves an existing directory.
	const fwdMid = "5000000000000030"
	const origAuthorUID = "3000000003"
	const forwarderUID = "2000000002"
	planner.setPlan(fwdMid, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:             site.Weibo,
			ItemKey:          "weibo:dynamic:" + fwdMid,
			TargetID:         fwdMid,
			Kind:             "image_note",
			AuthorUID:        origAuthorUID,
			AuthorName:       "原作者",
			Title:            "转发微博测试",
			Desc:             "转发微博正文",
			PubTs:            1763700300,
			IsForward:        true,
			OriginID:         "5000000000000099",
			OriginAuthorUID:  origAuthorUID,
			OriginAuthorName: "原作者",
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_01_jpg"}},
			},
			Media: strictTransport,
		}, nil
	})
	tkFwd, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{
		TargetID: fwdMid,
		Via: &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      string(site.Weibo),
			UID:       forwarderUID,
			Name:      "转发博主",
			DynamicID: fwdMid,
		},
	})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork forward: %v", err)
	}
	doneFwd := waitForTaskTerminal(t, mgr, tkFwd.TaskID, 10*time.Second)
	if doneFwd.Status != "completed" {
		t.Fatalf("forward task status=%s err=%s", doneFwd.Status, doneFwd.ErrorMessage)
	}
	metaFwd, exists, err := libStore.GetItem("weibo:dynamic:" + fwdMid)
	if err != nil || !exists {
		t.Fatalf("GetItem forward: exists=%v err=%v", exists, err)
	}
	wantFwdDir := filepath.Join(libStore.CacheDir(), "weibo", forwarderUID, "forwards", fwdMid)
	if filepath.Clean(metaFwd.ItemDir) != filepath.Clean(wantFwdDir) {
		t.Fatalf("forward ItemDir = %q, want %q", metaFwd.ItemDir, wantFwdDir)
	}
	if metaFwd.Via == nil || metaFwd.Via.Reason != library.ViaForward || metaFwd.Via.UID != forwarderUID || metaFwd.Via.Site != "weibo" {
		t.Fatalf("unexpected forward via: %+v", metaFwd.Via)
	}
	if _, err := os.Stat(filepath.Join(libStore.CacheDir(), "weibo", origAuthorUID)); !os.IsNotExist(err) {
		t.Fatalf("original author folder must not be created for forwarded post, stat err=%v", err)
	}

	// Re-caching the already-cached forwarded post as a non-forward must stay in its existing folder.
	planner.setPlan(fwdMid, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.Weibo,
			ItemKey:    "weibo:dynamic:" + fwdMid,
			TargetID:   fwdMid,
			Kind:       "image_note",
			AuthorUID:  origAuthorUID,
			AuthorName: "原作者",
			Title:      "转发微博重缓存不移动",
			PubTs:      1763700300,
			IsForward:  false,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_01_jpg"}},
			},
			Media: strictTransport,
		}, nil
	})
	tkRecache, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: fwdMid})
	if err != nil {
		t.Fatalf("re-enqueue forward: %v", err)
	}
	doneRecache := waitForTaskTerminal(t, mgr, tkRecache.TaskID, 10*time.Second)
	if doneRecache.Status != "completed" {
		t.Fatalf("recache status=%s err=%s", doneRecache.Status, doneRecache.ErrorMessage)
	}
	metaRecache, _, err := libStore.GetItem("weibo:dynamic:" + fwdMid)
	if err != nil {
		t.Fatalf("GetItem after recache: %v", err)
	}
	if filepath.Clean(metaRecache.ItemDir) != filepath.Clean(wantFwdDir) {
		t.Fatalf("already-cached entry moved from %q to %q", wantFwdDir, metaRecache.ItemDir)
	}
	if metaRecache.DynamicType != "forward" || metaRecache.TypeLabel != "转发" || metaRecache.Via == nil || metaRecache.Via.UID != forwarderUID {
		t.Fatalf("re-caching already-cached forward lost forward classification or via: dt=%q label=%q via=%+v", metaRecache.DynamicType, metaRecache.TypeLabel, metaRecache.Via)
	}

	// Subtest D: Original post cached first, then re-enqueued via fast-retweet must NOT
	// overwrite its directory, via (stays nil), dynamicType (stays "draw"), or author ownership.
	const origFirstMid = "5000000000000031"
	planner.setPlan(origFirstMid, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.Weibo,
			ItemKey:    "weibo:dynamic:" + origFirstMid,
			TargetID:   origFirstMid,
			Kind:       "image_note",
			AuthorUID:  origAuthorUID,
			AuthorName: "原作者",
			Title:      "原帖先缓存",
			Desc:       "原帖正文",
			PubTs:      1763700350,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_01_jpg"}},
			},
			Media: strictTransport,
		}, nil
	})
	tkOrig, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: origFirstMid})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork origFirst: %v", err)
	}
	if doneOrig := waitForTaskTerminal(t, mgr, tkOrig.TaskID, 10*time.Second); doneOrig.Status != "completed" {
		t.Fatalf("origFirst status=%s err=%s", doneOrig.Status, doneOrig.ErrorMessage)
	}
	wantOrigDir := filepath.Join(libStore.CacheDir(), "weibo", origAuthorUID, "posts", origFirstMid)

	// Now re-enqueue the exact same post via a fast-retweet with forwarder via.
	planner.setPlan(origFirstMid, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:          site.Weibo,
			ItemKey:       "weibo:dynamic:" + origFirstMid,
			TargetID:      origFirstMid,
			Kind:          "image_note",
			AuthorUID:     origAuthorUID,
			AuthorName:    "原作者",
			Title:         "快转重入队测试",
			Desc:          "原帖正文",
			PubTs:         1763700350,
			IsForward:     true,
			IsFastRetweet: true,
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", ImageURLs: []string{"https://wx1.sinaimg.cn/large/pic_01_jpg"}},
			},
			Media: strictTransport,
		}, nil
	})
	tkFastRetweet, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{
		TargetID: origFirstMid,
		Via: &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      string(site.Weibo),
			UID:       forwarderUID,
			Name:      "转发博主",
			DynamicID: origFirstMid,
		},
	})
	if err != nil {
		t.Fatalf("re-enqueue origFirst via fast-retweet: %v", err)
	}
	if doneFR := waitForTaskTerminal(t, mgr, tkFastRetweet.TaskID, 10*time.Second); doneFR.Status != "completed" {
		t.Fatalf("fast-retweet recache status=%s err=%s", doneFR.Status, doneFR.ErrorMessage)
	}
	metaOrigAfter, exists, err := libStore.GetItem("weibo:dynamic:" + origFirstMid)
	if err != nil || !exists {
		t.Fatalf("GetItem origFirst after fast-retweet: exists=%v err=%v", exists, err)
	}
	if filepath.Clean(metaOrigAfter.ItemDir) != filepath.Clean(wantOrigDir) {
		t.Fatalf("origFirst moved from %q to %q", wantOrigDir, metaOrigAfter.ItemDir)
	}
	if metaOrigAfter.Via != nil {
		t.Fatalf("fast-retweet overwrote original post via: %+v", metaOrigAfter.Via)
	}
	if metaOrigAfter.DynamicType == "forward" || metaOrigAfter.DynamicType != "draw" || metaOrigAfter.TypeLabel != "图文" {
		t.Fatalf("fast-retweet corrupted original post dynamicType/typeLabel: dt=%q label=%q", metaOrigAfter.DynamicType, metaOrigAfter.TypeLabel)
	}
	counts, err := libStore.CountCachedWorks([]library.AuthorRef{
		{Site: "weibo", UID: origAuthorUID},
		{Site: "weibo", UID: forwarderUID},
	})
	if err != nil {
		t.Fatalf("CountCachedWorks: %v", err)
	}
	if counts["weibo:"+origAuthorUID] != 1 || counts["weibo:"+forwarderUID] != 1 {
		t.Fatalf("unexpected CountCachedWorks after fast-retweet recache: %+v (want orig=1, forwarder=1)", counts)
	}

	// RebuildIndex + media library filters must include Weibo posts, mixed video, and forwards.
	if _, err := libStore.RebuildIndex(); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	vidItems, err := libStore.ListItems(library.FeedFilter{Site: "weibo", Kind: "video", Limit: 20})
	if err != nil || len(vidItems) != 1 || vidItems[0].ItemKey != "weibo:dynamic:"+richMid {
		t.Fatalf("weibo video filter after rebuild = %+v err=%v", vidItems, err)
	}
	fwdItems, err := libStore.ListItems(library.FeedFilter{Site: "weibo", Kind: "forward", Limit: 20})
	if err != nil || len(fwdItems) != 1 || fwdItems[0].ItemKey != "weibo:dynamic:"+fwdMid {
		t.Fatalf("weibo forward filter after rebuild = %+v err=%v", fwdItems, err)
	}

	// Zero signed-URL sentinels persisted anywhere.
	assertNoSentinelsInTreeAndDB(
		t,
		libStore.CacheDir(),
		dbPath,
		eventBuf.String(),
		[]string{liveSig, mixSig, "ssig=", "Expires=", "KID=", "ssig%3D", "Expires%3D", "KID%3D", "001live.mov", "mixed_10.mp4"},
		[]string{"video.weibo.com", "weibocdn.com"},
	)
}

func TestMultiSiteDispatchUnknownRejectionAndWeiboCircuitBreakerIsolation(t *testing.T) {
	mp4Bytes := buildProgressiveWithoutElst(t, true, false, 0)
	douyinStarted := make(chan struct{}, 1)
	douyinRelease := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/weibo/risk.mp4":
			w.WriteHeader(http.StatusTeapot) // 418 -> ErrKindRiskControl
		case "/douyin/ok.mp4":
			select {
			case douyinStarted <- struct{}{}:
			default:
			}
			<-douyinRelease
			serveRangeBytes(w, r, mp4Bytes, "video/mp4", 0, nil)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mgr, _, srv, _, _ := setupDouyinDownloadTestEnv(t, handler, 2)
	wbPlanner := newFakeWeiboSitePlanner()
	dyPlanner := newFakeDouyinWorkPlanner()
	mgr.RegisterSitePlanner(wbPlanner)
	mgr.RegisterWorkPlanner(dyPlanner)

	// 1. Unknown site rejected explicitly in enqueueSingleTaskWithPlan AND runTask (never falls back to Bilibili).
	if _, err := mgr.enqueueSingleTaskWithPlan(Task{
		Site:     "unknown_site",
		Kind:     "video",
		TargetID: "12345",
		Title:    "未知站点",
	}, nil); err == nil || !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("enqueueSingleTaskWithPlan unknown site = %v, want ErrKindBadRequest", err)
	}

	badTask := Task{
		TaskID:   "task-unknown-direct",
		Site:     "unknown_site",
		Kind:     "video",
		TargetID: "12345",
		Status:   "running",
	}
	// Persist row so finishTaskUpdate can mark it failed cleanly without touching Bilibili.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := mgr.db.Exec(`INSERT INTO download_tasks (task_id, site, kind, source, target_id, page, title, status, created_at, updated_at) VALUES (?, ?, ?, 'manual', ?, 1, 'bad', 'running', ?, ?)`,
		badTask.TaskID, badTask.Site, badTask.Kind, badTask.TargetID, now, now); err != nil {
		t.Fatalf("insert unknown task row: %v", err)
	}
	mgr.runTask(context.Background(), badTask)
	detailBad, ok, err := mgr.TaskDetail(badTask.TaskID)
	if err != nil || !ok {
		t.Fatalf("TaskDetail(bad): ok=%v err=%v", ok, err)
	}
	if detailBad.Task.Status != "failed" || detailBad.Task.ErrorKind != site.ErrKindBadRequest {
		t.Fatalf("unknown site runTask got status=%q kind=%q, want failed/bad_request", detailBad.Task.Status, detailBad.Task.ErrorKind)
	}

	// 2. Weibo risk_control pauses ONLY Weibo tasks, leaving Douyin task unaffected and completing.
	wbLocal := site.WeiboMediaTransport("TRACE-Test-UA", true)
	dyLocal := site.DouyinVODTransport("TRACE-Test-UA", true)

	const dyID = "7400000000000000101"
	const dyUID = "MS4wLjABAAAA_test_douyin_isolation_001"
	dyPlanner.setPlan(dyID, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:        site.Douyin,
			Kind:        "video",
			TargetID:    dyID,
			AuthorUID:   dyUID,
			AuthorName:  "抖音合成作者",
			Title:       "并发隔离抖音视频",
			DurationSec: 2,
			VideoURLs:   []string{srv.URL + "/douyin/ok.mp4"},
			Transport:   dyLocal,
		}, nil
	})
	tDouyin, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: dyID})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork: %v", err)
	}
	select {
	case <-douyinStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Douyin task to start")
	}

	// With MaxConcurrentTasks=2 and Douyin occupying 1 slot, wb1 runs immediately while wb2 stays queued.
	const wbMid1 = "5000000000000041"
	const wbMid2 = "5000000000000042"
	wbPlanner.setPlan(wbMid1, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:        site.Weibo,
			ItemKey:     "weibo:dynamic:" + wbMid1,
			TargetID:    wbMid1,
			Kind:        "single_mp4",
			AuthorUID:   "1000000001",
			AuthorName:  "合成博主",
			Title:       "风控触发微博1",
			DurationSec: 2,
			VideoURLs:   []string{srv.URL + "/weibo/risk.mp4"},
			Media:       wbLocal,
		}, nil
	})
	wbPlanner.setPlan(wbMid2, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:        site.Weibo,
			ItemKey:     "weibo:dynamic:" + wbMid2,
			TargetID:    wbMid2,
			Kind:        "single_mp4",
			AuthorUID:   "1000000001",
			AuthorName:  "合成博主",
			Title:       "风控排队微博2",
			DurationSec: 2,
			VideoURLs:   []string{srv.URL + "/weibo/risk.mp4"},
			Media:       wbLocal,
		}, nil
	})

	tWB1, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: wbMid1})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork wb1: %v", err)
	}
	tWB2, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: wbMid2})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork wb2: %v", err)
	}

	doneWB1 := waitForTaskTerminal(t, mgr, tWB1.TaskID, 10*time.Second)
	doneWB2 := waitForTaskTerminal(t, mgr, tWB2.TaskID, 10*time.Second)
	if doneWB1.Status != "paused" || doneWB1.PauseReason != site.ErrKindRiskControl {
		t.Fatalf("wb1 status=%q pauseReason=%q, want paused/risk_control", doneWB1.Status, doneWB1.PauseReason)
	}
	if doneWB2.Status != "paused" || doneWB2.PauseReason != site.ErrKindRiskControl {
		t.Fatalf("wb2 status=%q pauseReason=%q, want paused/risk_control", doneWB2.Status, doneWB2.PauseReason)
	}
	if wbPlanner.reportedCount() == 0 {
		t.Fatal("expected Weibo risk_control error to be reported to Weibo planner")
	}

	// Release Douyin task and confirm it completes unaffected by Weibo's circuit break.
	close(douyinRelease)
	doneDY := waitForTaskTerminal(t, mgr, tDouyin.TaskID, 10*time.Second)
	if doneDY.Status != "completed" {
		t.Fatalf("Douyin task affected by Weibo risk_control: status=%q pauseReason=%q err=%q", doneDY.Status, doneDY.PauseReason, doneDY.ErrorMessage)
	}
}

func TestWeiboRetweetOriginMetadataAndForwarderDirectoryPlacement(t *testing.T) {
	mp4Payload := buildProgressiveWithoutElst(t, true, true, 8*1024)
	jpegPayload := syntheticJPEGMini()
	qtMovPayload, _ := buildSyntheticQuickTimeLivePhotoMov(t, true, true)

	const (
		subUID  = "1000000051"
		origUID = "1000000099"
		sigTok  = "RetweetSecretSig9988"
		subTok  = "RetweetCookieSub7766"
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large/outer.jpg", "/large/orig1.jpg", "/large/orig2.jpg", "/large/vcover.jpg", "/large/fast1.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(jpegPayload)))
			_, _ = w.Write(jpegPayload)
		case "/orig2.mov":
			serveRangeBytes(w, r, qtMovPayload, "video/quicktime", 0, nil)
		case "/o0/rt_video.mp4":
			serveRangeBytes(w, r, mp4Payload, "video/mp4", 0, nil)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mgr, libStore, _, dbPath, eventBuf := setupDouyinDownloadTestEnv(t, http.NotFoundHandler(), 2)
	mgr.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			resp.Request = req
			return resp, nil
		})
	})

	planner := newFakeWeiboSitePlanner()
	mgr.RegisterSitePlanner(planner)
	strictTransport := site.WeiboMediaTransport("TRACE-Test-UA", false)

	// Case A: Regular retweet with outer comment image + origin image + origin LivePhoto + origin article card.
	const midRegImages = "5000000000005101"
	planner.setPlan(midRegImages, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:               site.Weibo,
			ItemKey:            "weibo:dynamic:" + midRegImages,
			TargetID:           midRegImages,
			Kind:               "image_note",
			CanonicalURL:       "https://weibo.com/" + subUID + "/Q5101",
			AuthorUID:          subUID,
			AuthorName:         "订阅转发者",
			Title:              "外层转发评语",
			Desc:               "外层转发评语",
			PubTs:              1763800100,
			IsForward:          true,
			OriginID:           "5000000000009901",
			OriginMblogID:      "Q9901",
			OriginCanonicalURL: "https://weibo.com/" + origUID + "/Q9901?ssig=" + sigTok,
			OriginAuthorUID:    origUID,
			OriginAuthorName:   "原博主甲",
			OriginAuthorFace:   "https://tvax1.sinaimg.cn/orig.jpg?Expires=1999999999&ssig=" + sigTok,
			OriginPubTs:        1763790000,
			OriginText:         "完整的原博长文正文内容 https://f.video.weibocdn.com/x.mp4?ssig=" + sigTok + " SUB=" + subTok,
			OriginCard: &site.RichCardSummary{
				Kind:  "article",
				Label: "头条文章",
				Title: "原博头条文章",
				Desc:  "文章简介",
				URL:   "https://weibo.com/ttarticle/p/show?id=2309405000009901",
			},
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", FromOrigin: false, Width: 800, Height: 600, ImageURLs: []string{"https://wx1.sinaimg.cn/large/outer.jpg"}},
				{Index: 2, MediaRole: "image", FromOrigin: true, Width: 1200, Height: 900, ImageURLs: []string{"https://wx1.sinaimg.cn/large/orig1.jpg"}},
				{Index: 3, MediaRole: "live_photo", FromOrigin: true, Width: 1080, Height: 1440, DurationSec: 2, ImageURLs: []string{"https://wx1.sinaimg.cn/large/orig2.jpg"}, VideoURLs: []string{"https://livephoto.us.sinaimg.cn/orig2.mov?ssig=" + sigTok}},
			},
			Media: strictTransport,
		}, nil
	})

	tA, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: midRegImages})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork(regular images retweet): %v", err)
	}
	doneA := waitForTaskTerminal(t, mgr, tA.TaskID, 10*time.Second)
	if doneA.Status != "completed" {
		t.Fatalf("Case A status=%s err=%s", doneA.Status, doneA.ErrorMessage)
	}
	metaA, ok, err := libStore.GetItem("weibo:dynamic:" + midRegImages)
	if err != nil || !ok {
		t.Fatalf("GetItem(Case A): ok=%v err=%v", ok, err)
	}
	wantDirA := filepath.Join(libStore.CacheDir(), "weibo", subUID, "forwards", midRegImages)
	if filepath.Clean(metaA.ItemDir) != filepath.Clean(wantDirA) {
		t.Fatalf("Case A ItemDir=%q, want %q", metaA.ItemDir, wantDirA)
	}
	if metaA.Via == nil || metaA.Via.Reason != library.ViaForward || metaA.Via.UID != subUID {
		t.Fatalf("Case A Via=%+v, want forward/%s", metaA.Via, subUID)
	}
	if metaA.Origin == nil || metaA.Origin.Author.UID != origUID || !strings.Contains(metaA.Origin.Text, "完整的原博长文正文内容") || metaA.Origin.Card == nil || metaA.Origin.Card.Kind != "article" {
		t.Fatalf("Case A Origin=%+v", metaA.Origin)
	}
	if len(metaA.NotePages) != 3 || metaA.NotePages[0].FromOrigin || !metaA.NotePages[1].FromOrigin || !metaA.NotePages[2].FromOrigin {
		t.Fatalf("Case A NotePages FromOrigin mismatch: %+v", metaA.NotePages)
	}

	// Case B: Regular retweet of single video -> video.mp4 & cover.jpg under subUID/forwards/, Origin.VideoFromOrigin=true.
	const midRegVideo = "5000000000005102"
	planner.setPlan(midRegVideo, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:             site.Weibo,
			ItemKey:          "weibo:dynamic:" + midRegVideo,
			TargetID:         midRegVideo,
			Kind:             "single_mp4",
			CanonicalURL:     "https://weibo.com/" + subUID + "/Q5102",
			AuthorUID:        subUID,
			AuthorName:       "订阅转发者",
			Title:            "转发微博",
			Desc:             "转发微博",
			PubTs:            1763800200,
			DurationSec:      2,
			VideoCodec:       "avc",
			QualityLabel:     "1080P",
			Width:            1920,
			Height:           1080,
			IsForward:        true,
			OriginID:         "5000000000009902",
			OriginAuthorUID:  origUID,
			OriginAuthorName: "原视频作者",
			OriginPubTs:      1763795000,
			OriginText:       "原视频完整文字描述",
			OriginVideoTitle: "原视频清晰标题",
			VideoFromOrigin:  true,
			VideoURLs:        []string{"https://f.video.weibocdn.com/o0/rt_video.mp4?ssig=" + sigTok + "&Expires=1999999999&KID=unistore,video"},
			CoverURLs:        []string{"https://wx1.sinaimg.cn/large/vcover.jpg"},
			Media:            strictTransport,
		}, nil
	})
	tB, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: midRegVideo})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork(regular video retweet): %v", err)
	}
	doneB := waitForTaskTerminal(t, mgr, tB.TaskID, 10*time.Second)
	if doneB.Status != "completed" {
		t.Fatalf("Case B status=%s err=%s", doneB.Status, doneB.ErrorMessage)
	}
	metaB, ok, err := libStore.GetItem("weibo:dynamic:" + midRegVideo)
	if err != nil || !ok {
		t.Fatalf("GetItem(Case B): ok=%v err=%v", ok, err)
	}
	wantDirB := filepath.Join(libStore.CacheDir(), "weibo", subUID, "forwards", midRegVideo)
	if filepath.Clean(metaB.ItemDir) != filepath.Clean(wantDirB) {
		t.Fatalf("Case B ItemDir=%q, want %q", metaB.ItemDir, wantDirB)
	}
	if metaB.Origin == nil || !metaB.Origin.VideoFromOrigin || metaB.Origin.Title != "原视频清晰标题" || metaB.Origin.Text != "原视频完整文字描述" {
		t.Fatalf("Case B Origin=%+v", metaB.Origin)
	}
	var sawOrigVideoMedia bool
	for _, mf := range metaB.Media {
		if mf.Role == "video" && mf.FromOrigin {
			sawOrigVideoMedia = true
		}
	}
	if !sawOrigVideoMedia {
		t.Fatalf("Case B expected video MediaFile.FromOrigin=true, got %+v", metaB.Media)
	}

	// Case C: Fast-retweet (plan.AuthorUID = origUID, req.Via = {ViaForward, UID = subUID}) MUST land under subUID/forwards/, NEVER origUID!
	const midFast = "5000000000005103"
	planner.setPlan(midFast, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:          site.Weibo,
			ItemKey:       "weibo:dynamic:" + midFast,
			TargetID:      midFast,
			Kind:          "image_note",
			CanonicalURL:  "https://weibo.com/" + origUID + "/Q5103",
			AuthorUID:     origUID, // fast-retweet carries original author on post.Author!
			AuthorName:    "原博主甲",
			Title:         "快转的完整图文正文",
			Desc:          "快转的完整图文正文",
			PubTs:         1763800300,
			IsForward:     true,
			IsFastRetweet: true,
			OriginID:      "5000000000009903",
			Images: []site.MediaImageAsset{
				{Index: 1, MediaRole: "image", Width: 800, Height: 600, ImageURLs: []string{"https://wx1.sinaimg.cn/large/fast1.jpg"}},
			},
			Media: strictTransport,
		}, nil
	})
	tC, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{
		TargetID: midFast,
		Via:      &library.ItemVia{Reason: library.ViaForward, Site: "weibo", UID: subUID, Name: "订阅转发者", DynamicID: midFast},
	})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork(fast-retweet): %v", err)
	}
	doneC := waitForTaskTerminal(t, mgr, tC.TaskID, 10*time.Second)
	if doneC.Status != "completed" {
		t.Fatalf("Case C status=%s err=%s", doneC.Status, doneC.ErrorMessage)
	}
	metaC, ok, err := libStore.GetItem("weibo:dynamic:" + midFast)
	if err != nil || !ok {
		t.Fatalf("GetItem(Case C): ok=%v err=%v", ok, err)
	}
	wantDirC := filepath.Join(libStore.CacheDir(), "weibo", subUID, "forwards", midFast)
	if filepath.Clean(metaC.ItemDir) != filepath.Clean(wantDirC) {
		t.Fatalf("Case C ItemDir=%q, want %q", metaC.ItemDir, wantDirC)
	}
	if metaC.Via == nil || metaC.Via.Reason != library.ViaForward || metaC.Via.UID != subUID {
		t.Fatalf("Case C Via=%+v, want forward/%s", metaC.Via, subUID)
	}

	// Verify ZERO files or directories were ever created under the original author's folder!
	origAuthorDir := filepath.Join(libStore.CacheDir(), "weibo", origUID)
	if _, statErr := os.Stat(origAuthorDir); !os.IsNotExist(statErr) {
		t.Fatalf("retweet caching MUST NEVER create original author folder %q (statErr=%v)", origAuthorDir, statErr)
	}

	assertNoSentinelsInTreeAndDB(t, libStore.CacheDir(), dbPath, eventBuf.String(),
		[]string{sigTok, subTok},
		[]string{"ssig=", "Expires=", "KID=", "weibocdn.com"},
	)
}

func waitForTaskTerminal(t *testing.T, mgr *Manager, taskID string, timeout time.Duration) Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		detail, ok, err := mgr.TaskDetail(taskID)
		if err == nil && ok {
			switch detail.Task.Status {
			case "completed", "partial", "failed", "paused", "canceled":
				return detail.Task
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	detail, _, _ := mgr.TaskDetail(taskID)
	t.Fatalf("timed out waiting for task %s to finish; last status=%q err=%q", taskID, detail.Task.Status, detail.Task.ErrorMessage)
	return Task{}
}

func assertNoSentinelsInTreeAndDB(t *testing.T, cacheDir, dbPath, eventLog string, globalSentinels, manifestOnlySentinels []string) {
	t.Helper()
	checkContent := func(label, text string, list []string) {
		for _, s := range list {
			if strings.Contains(text, s) {
				t.Errorf("leaked sentinel %q in %s", s, label)
			}
		}
	}
	checkContent("task event log", eventLog, globalSentinels)

	if err := filepath.WalkDir(cacheDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.EqualFold(filepath.Base(path), "item.json") {
			raw, rErr := os.ReadFile(path)
			if rErr != nil {
				return rErr
			}
			checkContent(path, string(raw), globalSentinels)
			checkContent(path, string(raw), manifestOnlySentinels)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk cacheDir: %v", err)
	}

	// Scan raw SQLite file (including WAL if present) for leaked sentinels.
	for _, p := range []string{dbPath, dbPath + "-wal"} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		raw, rErr := io.ReadAll(f)
		_ = f.Close()
		if rErr == nil {
			checkContent(p, string(raw), globalSentinels)
		}
	}
}
