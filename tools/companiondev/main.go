// Command companiondev launches a self-contained TRACE desktop companion server
// backed by a temporary data directory and synthetic multi-site media library,
// so the Android mobile app can be developed and tested on an emulator (reaching
// the host's 127.0.0.1 via 10.0.2.2) or LAN without touching real user data or
// sending any site requests.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/companion"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

func main() {
	var (
		listenHost  = flag.String("listen", "127.0.0.1", "IP address to bind (use 127.0.0.1 for Android emulator 10.0.2.2, or 0.0.0.0 / LAN IP for physical phone)")
		advertise   = flag.String("advertise", "10.0.2.2", "comma-separated address(es) advertised in the QR payload (default 10.0.2.2 for Android emulator)")
		port        = flag.Int("port", companionapi.DefaultDevPort, "HTTPS port to listen on")
		channel     = flag.String("channel", companionapi.ChannelDev, "companion channel (dev or stable)")
		nodeName    = flag.String("name", "TRACE Dev Desktop", "desktop display name")
		autoApprove = flag.Bool("auto-approve", true, "automatically approve incoming phone pairing requests")
		prePair     = flag.Bool("prepair", false, "also pre-pair a dev phone and print its bearer token for quick testing")
	)
	flag.Parse()

	tmpRoot, err := os.MkdirTemp("", "trace-companiondev-")
	if err != nil {
		log.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpRoot)

	dataDir := filepath.Join(tmpRoot, "data")
	cacheDir := filepath.Join(tmpRoot, "cache")
	loc := appdirs.Locations{
		Mode:            "portable",
		Channel:         *channel,
		AppName:         "trace-dev",
		DisplayName:     *nodeName,
		Identifier:      "io.github.chialecode.trace-dev",
		DataDir:         dataDir,
		DefaultCacheDir: cacheDir,
		CredentialsDir:  filepath.Join(dataDir, "credentials"),
		LogsDir:         filepath.Join(dataDir, "logs"),
		TempDir:         filepath.Join(tmpRoot, "tmp"),
		LockFile:        filepath.Join(dataDir, "app.lock"),
	}

	// Synthetic local-only Bilibili client stub so seed login never touches real sites.
	backend, err := service.NewBackend(service.Options{
		Locations:        loc,
		DisableBgWorkers: true,
		NodeName:         *nodeName,
		BilibiliOpts: bilibili.ClientOptions{
			WrapTransport: func(http.RoundTripper) http.RoundTripper {
				return syntheticAuthRoundTripper{}
			},
		},
	})
	if err != nil {
		log.Fatalf("init backend: %v", err)
	}
	defer backend.Close()

	if err := seedSyntheticData(backend, dataDir, cacheDir); err != nil {
		log.Fatalf("seed synthetic data: %v", err)
	}

	var advAddrs []string
	for _, a := range strings.Split(*advertise, ",") {
		if s := strings.TrimSpace(a); s != "" {
			advAddrs = append(advAddrs, s)
		}
	}

	var mgr *companion.Manager
	onChange := func() {
		if !*autoApprove || mgr == nil {
			return
		}
		snap := mgr.Status()
		for _, p := range snap.PendingPairs {
			dev, err := mgr.ApprovePairing(p.PairID)
			if err != nil {
				log.Printf("[companiondev] auto-approve %s failed: %v", p.PairID, err)
			} else {
				log.Printf("[companiondev] auto-approved %q (id=%s, addr=%s)", dev.Name, dev.ID, dev.LastAddr)
			}
		}
	}

	mgr, err = companion.NewManager(companion.ManagerOptions{
		DataDir:            dataDir,
		Channel:            *channel,
		NodeName:           *nodeName,
		Version:            "0.1.0-dev.companiondev",
		Backend:            backend,
		ListenOverrides:    []string{*listenHost},
		AdvertiseOverrides: advAddrs,
		OnChange:           onChange,
		Logf:               func(format string, args ...any) { log.Printf("[companion] "+format, args...) },
	})
	if err != nil {
		log.Fatalf("init companion manager: %v", err)
	}
	if err := mgr.ApplySettings(true, *port); err != nil {
		log.Fatalf("start companion listener: %v", err)
	}
	defer mgr.Shutdown(companionapi.ByeReasonExit)

	session, err := mgr.StartPairing()
	if err != nil {
		log.Fatalf("start pairing: %v", err)
	}
	snap := mgr.Status()

	fmt.Println("============================================================")
	fmt.Printf("TRACE companiondev ready (channel=%s)\n", snap.Channel)
	fmt.Printf("  Listen:        https://%s:%d\n", *listenHost, snap.Port)
	fmt.Printf("  Advertise:     %s:%d\n", strings.Join(session.Addrs, ", "), session.Port)
	fmt.Printf("  Fingerprint:   %s\n", snap.Fingerprint)
	fmt.Printf("  IdentityCode:  %s\n", session.IdentityCode)
	fmt.Printf("  ShortCode:     %s\n", session.ShortCode)
	fmt.Printf("  QR Payload:    %s\n", session.Payload)
	fmt.Printf("  AutoApprove:   %v\n", *autoApprove)
	if *prePair {
		dev, tok, err := mgr.Store().AddDevice("Dev Emulator", "Android Emulator", "android", snap.Channel, "127.0.0.1", time.Now())
		if err != nil {
			log.Fatalf("prepair failed: %v", err)
		}
		fmt.Printf("  PrePaired ID:  %s\n", dev.ID)
		fmt.Printf("  Bearer Token:  %s\n", tok)
	}
	fmt.Println("============================================================")
	fmt.Println("Press Ctrl+C to stop (sends bye{reason:\"exit\"} to connected clients)...")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	fmt.Println("\nShutting down companiondev...")
}

type syntheticAuthRoundTripper struct{}

func (syntheticAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body := `{"code":0,"message":"0","data":{"isLogin":true,"mid":10001,"uname":"合成测试账号","face":""}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       &nopReadCloser{bytes.NewBufferString(body)},
		Request:    req,
	}, nil
}

type nopReadCloser struct{ *bytes.Buffer }

func (*nopReadCloser) Close() error { return nil }

func seedSyntheticData(backend *service.Backend, dataDir, cacheDir string) error {
	if _, err := backend.ImportCookie(context.Background(), "SESSDATA=synth_dev_cookie; bili_jct=synth_csrf; DedeUserID=10001", ""); err != nil {
		return fmt.Errorf("seed login: %w", err)
	}

	// Create local PNG avatars using the exact hash naming of service.AccountAvatarPath
	// (<site>-<sha256(site+":"+uid)[:10]>.png) so /api/v1/avatars/{site}/{uid} serves offline PNGs.
	avatarsDir := filepath.Join(dataDir, "avatars")
	_ = os.MkdirAll(avatarsDir, 0o700)
	writeColorPNG(filepath.Join(avatarsDir, avatarFileName("bilibili", "1001")), color.RGBA{0x1a, 0x73, 0xe8, 0xff})
	writeColorPNG(filepath.Join(avatarsDir, avatarFileName("douyin", "MS4wLjABAAAA_SynthDevDouyin001")), color.RGBA{0xc2, 0x41, 0x0c, 0xff})
	writeColorPNG(filepath.Join(avatarsDir, avatarFileName("weibo", "500000001")), color.RGBA{0xe6, 0x39, 0x46, 0xff})

	// Seed subscriptions.
	if _, err := backend.CreateSubscription("技术频道 Alpha", "合成 B 站与抖音示例订阅", []subscription.AccountRef{
		{Site: "bilibili", UID: "1001", Name: "Alpha_B站"},
		{Site: "douyin", UID: "MS4wLjABAAAA_SynthDevDouyin001", Name: "Alpha_抖音", UniqueID: "alpha_dy"},
	}, subscription.SubRules{ScheduledCheck: true, AutoRecord: true}); err != nil {
		return fmt.Errorf("seed sub 1: %w", err)
	}
	if _, err := backend.CreateSubscription("图影日志 Beta", "微博与 X 合成示例", []subscription.AccountRef{
		{Site: "weibo", UID: "500000001", Name: "Beta微博"},
		{Site: "x", UID: "1000000000000000001", Name: "BetaX", UniqueID: "beta_x"},
	}, subscription.SubRules{ScheduledCheck: true}); err != nil {
		return fmt.Errorf("seed sub 2: %w", err)
	}

	lib := backend.LibraryStore()
	now := time.Now().Unix()

	// 1. Bilibili video with valid synthetic MP4 + JPEG cover.
	vDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1SYNTHDEV01")
	_ = os.MkdirAll(vDir, 0o700)
	coverBytes := makeJPEGBytes(640, 360, color.RGBA{0x2b, 0x6c, 0xb0, 0xff})
	coverPath := filepath.Join(vDir, "cover.jpg")
	if err := os.WriteFile(coverPath, coverBytes, 0o600); err != nil {
		return err
	}
	mp4Bytes := makeSyntheticMP4Bytes()
	mp4Path := filepath.Join(vDir, "P01.mp4")
	if err := os.WriteFile(mp4Path, mp4Bytes, 0o600); err != nil {
		return err
	}
	if _, err := lib.SaveItem(vDir, library.ItemMetadata{
		Version:  1,
		ID:       "BV1SYNTHDEV01",
		Site:     "bilibili",
		Kind:     "video",
		Status:   "complete",
		Title:    "合成示例视频：局域网移动端 Range 播放测试",
		Desc:     "用于本地移动端联调的合成 MP4 与封面样本。",
		URL:      "https://www.bilibili.com/video/BV1SYNTHDEV01",
		PubTs:    now - 120,
		Duration: 60,
		Author:   library.ItemAuthor{Site: "bilibili", UID: "1001", Mid: 1001, Name: "Alpha_B站"},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.jpg", Bytes: int64(len(coverBytes)), SHA256: sha256Hex(coverBytes), Width: 640, Height: 360},
			{Role: "video", Path: "P01.mp4", Bytes: int64(len(mp4Bytes)), SHA256: sha256Hex(mp4Bytes), Duration: 60},
		},
		Parts: []library.VideoPartRecord{{
			CID: 9001, Page: 1, Title: "第 1 P", Duration: 60, File: "P01.mp4",
			Video: library.PartVideoInfo{Quality: 80, QualityName: "1080P", Codecs: "avc1", Width: 1920, Height: 1080},
			Audio: library.PartAudioInfo{Quality: 30280, Name: "192K"},
		}},
	}); err != nil {
		return fmt.Errorf("save video item: %w", err)
	}

	// 2. Weibo dual-image post (original + watermark-free companion).
	wbDir := filepath.Join(cacheDir, "weibo", "500000001", "posts", "500100200")
	_ = os.MkdirAll(filepath.Join(wbDir, "images"), 0o700)
	img1 := makeJPEGBytes(1200, 900, color.RGBA{0x0a, 0x87, 0x54, 0xff})
	img1NoWM := makeJPEGBytes(690, 518, color.RGBA{0x12, 0xa1, 0x67, 0xff})
	_ = os.WriteFile(filepath.Join(wbDir, "images", "01.jpg"), img1, 0o600)
	_ = os.WriteFile(filepath.Join(wbDir, "images", "01_nowm.jpg"), img1NoWM, 0o600)
	if _, err := lib.SaveItem(wbDir, library.ItemMetadata{
		Version:     1,
		ID:          "500100200",
		Site:        "weibo",
		Kind:        "dynamic",
		Status:      "complete",
		Title:       "合成微博双图示例（原图 + 无水印伴侣）",
		Text:        "这是一条用于移动端看图与原图/无水印切换联调的合成微博帖子。",
		DynamicType: "draw",
		TypeLabel:   "图文",
		URL:         "https://weibo.com/500000001/500100200",
		PubTs:       now - 600,
		Author:      library.ItemAuthor{Site: "weibo", UID: "500000001", Name: "Beta微博"},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.jpg", Bytes: int64(len(img1)), SHA256: sha256Hex(img1), Width: 1200, Height: 900},
			{Role: "image_nowm", Path: "images/01_nowm.jpg", Bytes: int64(len(img1NoWM)), SHA256: sha256Hex(img1NoWM), Width: 690, Height: 518},
		},
		NotePages: []library.NotePageRecord{{
			Index: 1, Kind: "image", Image: "images/01.jpg", NoWatermark: "images/01_nowm.jpg", Width: 1200, Height: 900,
		}},
	}); err != nil {
		return fmt.Errorf("save weibo item: %w", err)
	}

	// 3. Live recording item with 1 segment and 1 incident.
	liveDir := filepath.Join(cacheDir, "bilibili", "1001", "lives", "88001-20261126")
	_ = os.MkdirAll(liveDir, 0o700)
	_ = os.WriteFile(filepath.Join(liveDir, "cover.jpg"), coverBytes, 0o600)
	_ = os.WriteFile(filepath.Join(liveDir, "part-001.mp4"), mp4Bytes, 0o600)
	if _, err := lib.SaveItem(liveDir, library.ItemMetadata{
		Version: 1,
		ID:      "88001-20261126",
		Site:    "bilibili",
		Kind:    "live",
		Status:  "complete",
		Title:   "合成直播录像：夜间联调场",
		URL:     "https://live.bilibili.com/88001",
		PubTs:   now - 3600,
		Author:  library.ItemAuthor{Site: "bilibili", UID: "1001", Mid: 1001, Name: "Alpha_B站"},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.jpg", Bytes: int64(len(coverBytes)), SHA256: sha256Hex(coverBytes)},
			{Role: "live_segment", Path: "part-001.mp4", Bytes: int64(len(mp4Bytes)), SHA256: sha256Hex(mp4Bytes)},
		},
		Live: &library.LiveRecordingInfo{
			RoomID: 88001, RoomIDStr: "88001", LiveSince: now - 3600,
			StartedAt:   time.Unix(now-3600, 0).UTC().Format(time.RFC3339),
			EndedAt:     time.Unix(now-1800, 0).UTC().Format(time.RFC3339),
			QualityName: "原画", Codec: "AVC",
			Segments: []library.LiveSegmentInfo{{
				File: "part-001.mp4", Bytes: int64(len(mp4Bytes)), SHA256: sha256Hex(mp4Bytes),
				DurationSec: 1800, Codec: "avc1", Width: 1920, Height: 1080, QualityName: "原画",
			}},
			Incidents: []library.LiveIncident{{
				Kind: library.LiveIncidentReconnect, At: time.Unix(now-2400, 0).UTC().Format(time.RFC3339),
				DurationSec: 2.5, Count: 1, Reason: "合成断流重连示例",
			}},
		},
	}); err != nil {
		return fmt.Errorf("save live item: %w", err)
	}
	return nil
}

func makeJPEGBytes(w, h int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}

func writeColorPNG(path string, c color.RGBA) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	_ = os.WriteFile(path, buf.Bytes(), 0o600)
}

func makeSyntheticMP4Bytes() []byte {
	// Minimal ISO-BMFF container (ftyp + empty moov + 4 KiB mdat) for Range streaming tests.
	ftyp := []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p',
		'i', 's', 'o', 'm', 0x00, 0x00, 0x02, 0x00,
		'i', 's', 'o', 'm', 'm', 'p', '4', '1',
	}
	moov := []byte{0x00, 0x00, 0x00, 0x08, 'm', 'o', 'o', 'v'}
	payload := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 1024)
	mdatSize := uint32(8 + len(payload))
	mdatHdr := []byte{
		byte(mdatSize >> 24), byte(mdatSize >> 16), byte(mdatSize >> 8), byte(mdatSize),
		'm', 'd', 'a', 't',
	}
	var buf bytes.Buffer
	buf.Write(ftyp)
	buf.Write(moov)
	buf.Write(mdatHdr)
	buf.Write(payload)
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func avatarFileName(siteID, uid string) string {
	siteID = strings.ToLower(strings.TrimSpace(siteID))
	uid = strings.TrimSpace(uid)
	sum := sha256.Sum256([]byte(siteID + ":" + uid))
	return siteID + "-" + hex.EncodeToString(sum[:10]) + ".png"
}
