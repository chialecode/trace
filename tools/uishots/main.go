package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/shell"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/ui"
	"github.com/chialecode/trace/internal/update"
	mygoui "github.com/egoist/mygo/ui"
)

type SceneAssertion struct {
	Scene                   string       `json:"scene"`
	Width                   int          `json:"width"`
	Height                  int          `json:"height"`
	Screenshot              string       `json:"screenshot"`
	LeftRect                mygoui.Rect  `json:"leftRect"`
	CenterRect              mygoui.Rect  `json:"centerRect"`
	PanelRect               *mygoui.Rect `json:"panelRect,omitempty"`
	NoHorizontalOver        bool         `json:"noHorizontalOverflow"`
	FixedSidebarsOnScr      bool         `json:"fixedSidebarsOnScroll"`
	RequiredControlsVisible bool         `json:"requiredControlsVisible"`
	ForbiddenControlsAbsent bool         `json:"forbiddenControlsAbsent"`
	RequiredChecked         []string     `json:"requiredChecked,omitempty"`
	MissingLabels           []string     `json:"missingLabels,omitempty"`
	ForbiddenChecked        []string     `json:"forbiddenChecked,omitempty"`
	Passed                  bool         `json:"passed"`
}

type UIShotsReport struct {
	GeneratedAt                 string           `json:"generatedAt"`
	HeadlessTester              bool             `json:"headlessTester"`
	AudioMutedNullSink          bool             `json:"audioMutedNullSink"`
	VirtualFeedCount            int              `json:"virtualFeedCount"`
	ComposeGuardPassed          bool             `json:"composeGuardPassed"`
	SiteFiltersVerified         bool             `json:"siteFiltersVerified"`
	SiteBadgesInSixPlaces       bool             `json:"siteBadgesInSixPlaces"`
	SubscriptionGridNoSiteBadge bool             `json:"subscriptionGridNoSiteBadge"`
	Assertions                  []SceneAssertion `json:"assertions"`
	Passed                      bool             `json:"passed"`
}

// Screenshot-only live and multi-site states depict logged-in accounts, per-site backoff,
// and active recordings without starting background workers or real network requests.
type screenshotBackend struct {
	service.BackendAPI
	rooms      []live.RoomState
	dyStatus   credential.Status
	schedulers map[string]service.SiteSchedulerStatus
	liveAccts  []service.LiveAccountState
}

func (b *screenshotBackend) Statuses() map[string]credential.Status {
	bili := b.BackendAPI.StatusOf("bilibili")
	return map[string]credential.Status{
		"bilibili": bili,
		"douyin":   b.dyStatus,
	}
}

func (b *screenshotBackend) StatusOf(siteID string) credential.Status {
	if strings.EqualFold(strings.TrimSpace(siteID), "douyin") {
		return b.dyStatus
	}
	return b.BackendAPI.StatusOf(siteID)
}

func (b *screenshotBackend) HasAnyLogin() bool {
	return b.BackendAPI.HasAnyLogin() || b.dyStatus.LoggedIn
}

func (b *screenshotBackend) SchedulerStatuses() map[string]service.SiteSchedulerStatus {
	if len(b.schedulers) > 0 {
		out := make(map[string]service.SiteSchedulerStatus, len(b.schedulers))
		for k, v := range b.schedulers {
			out[k] = v
		}
		return out
	}
	return b.BackendAPI.SchedulerStatuses()
}

func (b *screenshotBackend) ListLiveAccounts() []service.LiveAccountState {
	if len(b.liveAccts) > 0 {
		return append([]service.LiveAccountState(nil), b.liveAccts...)
	}
	return b.BackendAPI.ListLiveAccounts()
}

func (b *screenshotBackend) LiveStatuses(_ context.Context, _ []string) ([]service.LiveAccountState, error) {
	if len(b.liveAccts) > 0 {
		return append([]service.LiveAccountState(nil), b.liveAccts...), nil
	}
	return nil, nil
}

func (b *screenshotBackend) LiveStatus(_ context.Context, ids []int64) ([]live.RoomState, error) {
	var out []live.RoomState
	for _, id := range ids {
		state := live.RoomState{UID: id}
		for _, fixture := range b.rooms {
			if fixture.UID == id {
				state = fixture
				break
			}
		}
		out = append(out, state)
	}
	return out, nil
}

func buildSyntheticWAVBytes(sampleRate, channels, numFrames int) []byte {
	bytesPerSample := 2
	dataBytes := numFrames * channels * bytesPerSample
	buf := make([]byte, 44+dataBytes)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataBytes))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1)
	binary.LittleEndian.PutUint16(buf[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*channels*bytesPerSample))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(channels*bytesPerSample))
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataBytes))
	off := 44
	for f := 0; f < numFrames; f++ {
		sample := int16(3000 * math.Sin(2*math.Pi*440*float64(f)/float64(sampleRate)))
		for c := 0; c < channels; c++ {
			binary.LittleEndian.PutUint16(buf[off:off+2], uint16(sample))
			off += 2
		}
	}
	return buf
}

func main() {
	outDir := flag.String("out", "artifacts/uishots", "output directory for UI screenshots and assertions.json")
	flag.Parse()

	_ = os.Setenv("TRACE_QUIET", "1")
	_ = shell.SetBelowNormalPriority()

	if err := runUIShots(*outDir); err != nil {
		fmt.Fprintf(os.Stderr, "uishots failed: %v\n", err)
		os.Exit(1)
	}
}

func runUIShots(outDir string) error {
	defer ui.UseAccent("blue")

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if entries, err := os.ReadDir(outDir); err == nil {
		for _, ent := range entries {
			if !ent.IsDir() {
				_ = os.Remove(filepath.Join(outDir, ent.Name()))
			}
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":10086,"uname":"TRACE演示账号","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmpRoot, err := os.MkdirTemp("", "trace-uishots-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpRoot)

	loc := appdirs.Locations{
		Channel:         "dev",
		Mode:            "dev",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		DataDir:         filepath.Join(tmpRoot, "data"),
		DefaultCacheDir: filepath.Join(tmpRoot, "cache"),
		CredentialsDir:  filepath.Join(tmpRoot, "data", "credentials"),
		LogsDir:         filepath.Join(tmpRoot, "data", "logs"),
		LockFile:        filepath.Join(tmpRoot, "data", "app.lock"),
	}

	desk := service.NewMockDesktop()
	b, err := service.NewBackend(service.Options{
		Locations: loc,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      srv.URL,
				PassportBase: srv.URL,
				WWWBase:      srv.URL,
				SpaceBase:    srv.URL,
				DynamicBase:  srv.URL,
				LiveAPIBase:  srv.URL,
				LiveWebBase:  srv.URL,
			},
			PacingInterval: time.Millisecond,
			AllowLocalhost: true,
		},
		Desktop:          desk,
		DisableBgWorkers: true,
	})
	if err != nil {
		return err
	}
	defer b.Close()

	// Seed synthetic Bilibili + Douyin data
	_, _ = b.ImportCookie(context.Background(), "SESSDATA=synth_sess; bili_jct=synth_csrf; DedeUserID=10086", "synth_refresh")
	douyinSecUID := "MS4wLjABAAAA_uishots_creator_01"
	demoCreators := []struct {
		Mid  int64
		Name string
		Note string
	}{
		{20001, "技术频道_Alpha", "系统编程与多媒体工程主号"},
		{20002, "独立游戏工坊", "每周开发日志与渲染管线直播"},
		{20003, "数字影像志", "4K HDR 建筑与风光摄影"},
		{20004, "开源架构师", "编译器与高性能网络组件"},
		{20005, "极客实验室", "NAS 与存储备份实测"},
		{20006, "深海摄影师", "水下摄影原图与调色分享"},
	}
	var primarySub subscription.Subscription
	for i, dc := range demoCreators {
		sub, _ := b.UpsertSubscription(subscription.Subscription{
			Site:           "bilibili",
			Mid:            dc.Mid,
			Uname:          dc.Name,
			Name:           dc.Name,
			Note:           dc.Note,
			Scope:          "both",
			AutoCache:      "all",
			LiveNotify:     true,
			LiveAutoRecord: dc.Mid == 20002,
			RoomID:         9000 + (dc.Mid - 20000),
			Enabled:        true,
		})
		if i == 0 {
			// Link a companion Douyin account on the first subscription to showcase multi-site accounts.
			linked, linkErr := b.LinkSubscriptionAccountRef(sub.ID, subscription.AccountRef{
				Site:     "douyin",
				UID:      douyinSecUID,
				UniqueID: "alpha_douyin",
				WebRID:   "668899",
				Name:     "技术频道_抖音号",
			})
			if linkErr == nil {
				primarySub = linked
			} else {
				primarySub = sub
			}
		}
	}
	_, _ = b.CreateSubscription("极光纪实", "高原延时与图文手记", []subscription.AccountRef{
		{
			Site:     "douyin",
			UID:      "MS4wLjABAAAA_uishots_creator_02",
			UniqueID: "aurora_doc",
			WebRID:   "778899",
			Name:     "极光纪实",
		},
	}, subscription.SubRules{ScheduledCheck: true})

	itemDir := filepath.Join(loc.DefaultCacheDir, "bilibili", "20001_技术频道_Alpha", "2026-03-01_video_BV1xx411c7mD_示例缓存视频")
	_ = os.MkdirAll(itemDir, 0o700)
	vBytes, err := download.BuildSyntheticVideoM4S(640, 360)
	if err != nil {
		return err
	}
	aBytes, err := download.BuildSyntheticAudioM4S(15)
	if err != nil {
		return err
	}
	vPath := filepath.Join(itemDir, "v.m4s")
	aPath := filepath.Join(itemDir, "a.m4s")
	mp4Path := filepath.Join(itemDir, "P01_第一集.mp4")
	_ = os.WriteFile(vPath, vBytes, 0o600)
	_ = os.WriteFile(aPath, aBytes, 0o600)
	if _, err := download.MuxDASHFiles(vPath, aPath, mp4Path); err != nil {
		return err
	}
	_ = os.Remove(vPath)
	_ = os.Remove(aPath)

	_ = os.WriteFile(filepath.Join(itemDir, "P01.danmaku.xml"), []byte(`<i><d p="0.2,1,25,16777215,1710000000,0,u1,1">本机缓存弹幕：4K HDR 无损合流</d><d p="0.8,1,25,16777215,1710000000,0,u2,2">纯 Go + MyGo 原生界面</d></i>`), 0o600)
	_ = os.WriteFile(filepath.Join(itemDir, "P01.zh-CN.vtt"), []byte("WEBVTT\n\n00:00:00.000 --> 00:00:05.000\n本机缓存 WebVTT 字幕：第一句\n"), 0o600)

	sampleImg := image.NewRGBA(image.Rect(0, 0, 320, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 320; x++ {
			sampleImg.SetRGBA(x, y, color.RGBA{
				R: uint8(214 - (x*80)/320),
				G: uint8(51 + (y*120)/200),
				B: uint8(108 + (x*90)/320),
				A: 255,
			})
		}
	}
	var coverBuf bytes.Buffer
	_ = png.Encode(&coverBuf, sampleImg)
	_ = os.WriteFile(filepath.Join(itemDir, "cover.png"), coverBuf.Bytes(), 0o600)

	portraitImg := image.NewRGBA(image.Rect(0, 0, 270, 360))
	for y := 0; y < 360; y++ {
		for x := 0; x < 270; x++ {
			portraitImg.SetRGBA(x, y, color.RGBA{
				R: uint8(36 + (x*90)/270),
				G: uint8(88 + (y*110)/360),
				B: uint8(168 - (y*40)/360),
				A: 255,
			})
		}
	}
	var portraitBuf bytes.Buffer
	_ = png.Encode(&portraitBuf, portraitImg)

	savedItem, _ := b.LibraryStore().SaveItem(itemDir, library.ItemMetadata{
		Version:  1,
		ID:       "BV1xx411c7mD",
		RawID:    "BV1xx411c7mD",
		Site:     "bilibili",
		Kind:     "video",
		Status:   "complete",
		Title:    "示例本地缓存视频：4K HDR 与 Hi-Res 封装验证",
		URL:      "https://www.bilibili.com/video/BV1xx411c7mD",
		PubTs:    1710000000,
		CachedAt: "2026-03-01T12:00:00Z",
		Author: library.ItemAuthor{
			Mid:  20001,
			Name: "技术频道_Alpha",
		},
		Media: []library.MediaFile{
			{Role: "video", Path: "P01_第一集.mp4"},
			{Role: "danmaku", Path: "P01.danmaku.xml"},
			{Role: "subtitle", Path: "P01.zh-CN.vtt"},
			{Role: "cover", Path: "cover.png"},
		},
		Parts: []library.VideoPartRecord{
			{
				CID:       101,
				Page:      1,
				Title:     "第一集：纯 Go 容器合成",
				Duration:  120,
				File:      "P01_第一集.mp4",
				Danmaku:   "P01.danmaku.xml",
				Subtitles: []string{"P01.zh-CN.vtt"},
				Audio: library.PartAudioInfo{
					Quality: 30251,
					Name:    "Hi-Res 无损",
					SavedAs: "muxed-flac",
				},
			},
		},
	})

	mp4Raw, err := os.ReadFile(mp4Path)
	if err != nil {
		return err
	}

	liveItemDir := filepath.Join(loc.DefaultCacheDir, "bilibili", "20001_技术频道_Alpha", "2026-03-01_live_9001_示例直播录像")
	_ = os.MkdirAll(liveItemDir, 0o700)
	_ = os.WriteFile(filepath.Join(liveItemDir, "P01_第一集.mp4"), mp4Raw, 0o600)
	_ = os.WriteFile(filepath.Join(liveItemDir, "cover.png"), coverBuf.Bytes(), 0o600)
	savedLiveItem, _ := b.LibraryStore().SaveItem(liveItemDir, library.ItemMetadata{
		Version:  1,
		ID:       "live_9001_1710001800",
		RawID:    "9001",
		Site:     "bilibili",
		Kind:     "live",
		Status:   "complete",
		Title:    "示例直播录像：原画 fMP4 自动分段归档",
		URL:      "https://live.bilibili.com/9001",
		PubTs:    1710001800,
		CachedAt: "2026-03-01T14:00:00Z",
		Duration: 120,
		Author: library.ItemAuthor{
			Mid:  20001,
			Name: "技术频道_Alpha",
		},
		Media: []library.MediaFile{
			{Role: "live_segment", Path: "P01_第一集.mp4", Duration: 120},
			{Role: "cover", Path: "cover.png"},
		},
		Live: &library.LiveRecordingInfo{
			RoomID: 9001,
			Segments: []library.LiveSegmentInfo{
				{File: "P01_第一集.mp4", DurationSec: 120, Quality: 10000, Codec: "avc"},
			},
		},
	})

	partialDir := filepath.Join(loc.DefaultCacheDir, "bilibili", "20001_技术频道_Alpha", "videos", "synthetic-partial-detail")
	if err := os.MkdirAll(partialDir, 0o700); err != nil {
		return err
	}
	for name, raw := range map[string][]byte{"P01.mp4": mp4Raw, "cover.png": coverBuf.Bytes()} {
		if err := os.WriteFile(filepath.Join(partialDir, name), raw, 0o600); err != nil {
			return err
		}
	}
	savedPartialItem, err := b.LibraryStore().SaveItem(partialDir, library.ItemMetadata{
		Site: "bilibili", Kind: "video", ID: "BV1PartialFixture", Title: "本机部分缓存视频：继续缓存缺失的分P",
		Author: library.ItemAuthor{Mid: 20001, Name: "技术频道_Alpha"}, PubTs: 1710000000, CachedAt: "2026-03-01T12:00:00Z",
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}, {Role: "cover", Path: "cover.png"}},
		Parts: []library.VideoPartRecord{{Page: 1, CID: 101, File: "P01.mp4", Title: "第一部分", Duration: 120}}, ExpectedPages: []int{1, 2},
	})
	if err != nil {
		return err
	}

	dynamicDir := filepath.Join(loc.DefaultCacheDir, "bilibili", "20001_技术频道_Alpha", "dynamics", "synthetic-local-detail")
	if err := os.MkdirAll(dynamicDir, 0o700); err != nil {
		return err
	}
	var dynamicImages []library.MediaFile
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("image-%d.png", i)
		if err := os.WriteFile(filepath.Join(dynamicDir, name), coverBuf.Bytes(), 0o600); err != nil {
			return err
		}
		dynamicImages = append(dynamicImages, library.MediaFile{Role: "image", Path: name, Width: 320, Height: 200})
	}
	// The first image is a live photo (实况) with its motion clip (DEC-046 ①).
	if err := os.WriteFile(filepath.Join(dynamicDir, "image-1_live.mp4"), mp4Raw, 0o600); err != nil {
		return err
	}
	dynamicImages[0].Motion = "image-1_live.mp4"
	dynamicImages = append(dynamicImages, library.MediaFile{Role: "motion", Path: "image-1_live.mp4", Duration: 3})
	savedDynamicItem, err := b.LibraryStore().SaveItem(dynamicDir, library.ItemMetadata{
		Site: "bilibili", Kind: "dynamic", ID: "synthetic-local-detail", Title: "本机动态详情：图片与引用视频",
		Text: "图片和引用视频均已缓存，点击即可在应用内查看。", PubTs: 1710002000, CachedAt: "2026-03-02T11:30:00Z",
		Author:      library.ItemAuthor{Mid: 20001, Name: "技术频道_Alpha"},
		DynamicType: "DYNAMIC_TYPE_AV", TypeLabel: "视频动态", Media: dynamicImages, Videos: []string{savedItem.RawID},
	})
	if err != nil {
		return err
	}

	// Seed cached Douyin video and mixed Douyin note (with image + live photo + video page + pure-audio WAV BGM)
	dyVidDir := filepath.Join(loc.DefaultCacheDir, "douyin", "a1b2c3d4e5f6", "videos", "7388001001")
	if err := os.MkdirAll(dyVidDir, 0o700); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(dyVidDir, "video.mp4"), mp4Raw, 0o600)
	_ = os.WriteFile(filepath.Join(dyVidDir, "cover.png"), portraitBuf.Bytes(), 0o600)
	savedDouyinVideo, err := b.LibraryStore().SaveItem(dyVidDir, library.ItemMetadata{
		Version:  1,
		ID:       "7388001001",
		RawID:    "7388001001",
		Site:     "douyin",
		Kind:     "video",
		Status:   "complete",
		Title:    "竖屏高原雪山延时摄影记录",
		Desc:     "竖屏高原雪山延时摄影记录\n无水印高码率原片归档 #延时摄影 #风光",
		URL:      "https://www.douyin.com/video/7388001001",
		PubTs:    1710003000,
		CachedAt: "2026-03-02T12:00:00Z",
		Duration: 95,
		Author: library.ItemAuthor{
			Site:     "douyin",
			UID:      douyinSecUID,
			UniqueID: "alpha_douyin",
			Name:     "技术频道_抖音号",
		},
		Media: []library.MediaFile{
			{Role: "video", Path: "video.mp4", Duration: 95},
			{Role: "cover", Path: "cover.png", Width: 1080, Height: 1440},
		},
		DouyinVideo: &library.DouyinVideoInfo{
			File:        "video.mp4",
			QualityName: "1080P",
			Codec:       "hevc",
			Width:       1080,
			Height:      1440,
		},
		Music: &library.MusicInfo{
			Title:    "高原风声",
			Author:   "技术频道_抖音号",
			Original: true,
		},
	})
	if err != nil {
		return err
	}

	dyNoteDir := filepath.Join(loc.DefaultCacheDir, "douyin", "a1b2c3d4e5f6", "notes", "7388001002")
	if err := os.MkdirAll(dyNoteDir, 0o700); err != nil {
		return err
	}
	wavRaw := buildSyntheticWAVBytes(48000, 2, 48000*4)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "p01.png"), portraitBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "p02.png"), portraitBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "p02_motion.mp4"), mp4Raw, 0o600)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "p03_cover.png"), portraitBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "p03_video.mp4"), mp4Raw, 0o600)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "music.wav"), wavRaw, 0o600)
	savedDouyinNote, err := b.LibraryStore().SaveItem(dyNoteDir, library.ItemMetadata{
		Version:  1,
		ID:       "7388001002",
		RawID:    "7388001002",
		Site:     "douyin",
		Kind:     "dynamic",
		Status:   "complete",
		Title:    "山谷图文实况与视频混排手记",
		Text:     "山谷图文实况与视频混排手记\n按原始页序保存静态图、实况与视频页 #图文手记 #实况",
		URL:      "https://www.douyin.com/note/7388001002",
		PubTs:    1710003600,
		CachedAt: "2026-03-02T11:00:00Z",
		Author: library.ItemAuthor{
			Site:     "douyin",
			UID:      douyinSecUID,
			UniqueID: "alpha_douyin",
			Name:     "技术频道_抖音号",
		},
		Media: []library.MediaFile{
			{Role: "image", Path: "p01.png", Width: 1080, Height: 1440},
			{Role: "image", Path: "p02.png", Motion: "p02_motion.mp4", Width: 1080, Height: 1440},
			{Role: "motion", Path: "p02_motion.mp4", Duration: 3},
			{Role: "cover", Path: "p03_cover.png", Width: 1080, Height: 1440},
			{Role: "video", Path: "p03_video.mp4", Duration: 18},
			{Role: "audio", Path: "music.wav", Duration: 4},
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "p01.png", Width: 1080, Height: 1440},
			{Index: 2, Kind: "motion", Image: "p02.png", Motion: "p02_motion.mp4", Width: 1080, Height: 1440, Duration: 3},
			{Index: 3, Kind: "video", Cover: "p03_cover.png", Video: "p03_video.mp4", Width: 1080, Height: 1440, Duration: 18},
		},
		Music: &library.MusicInfo{
			Title:    "山间晨雾",
			Author:   "技术频道_抖音号",
			Duration: 4,
			File:     "music.wav",
		},
	})
	if err != nil {
		return err
	}

	// Seed recent parse history with local avatar & cover thumbnails in <DataDir>/thumbs/ across Bilibili and Douyin
	avatarImg := image.NewRGBA(image.Rect(0, 0, 80, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 80; x++ {
			avatarImg.SetRGBA(x, y, color.RGBA{
				R: uint8(40 + (x*160)/80),
				G: uint8(90 + (y*100)/80),
				B: uint8(190),
				A: 255,
			})
		}
	}
	var avatarBuf bytes.Buffer
	_ = png.Encode(&avatarBuf, avatarImg)
	histAv1, _ := b.HistoryStore().SaveThumb("video", "BV1xx411c7mD", "avatar", ".png", avatarBuf.Bytes())
	histCv1, _ := b.HistoryStore().SaveThumb("video", "BV1xx411c7mD", "cover", ".png", coverBuf.Bytes())
	histAv2, _ := b.HistoryStore().SaveThumb("user", "20002", "avatar", ".png", avatarBuf.Bytes())
	histAvDy, _ := b.HistoryStore().SaveThumb("video", "7388001001", "avatar", ".png", avatarBuf.Bytes())
	histCvDy, _ := b.HistoryStore().SaveThumb("video", "7388001001", "cover", ".png", portraitBuf.Bytes())
	_ = b.HistoryStore().Record(storage.HistoryEntry{
		Site:         "douyin",
		Kind:         "video",
		ID:           "7388001001",
		Title:        "竖屏高原雪山延时摄影记录",
		Subtitle:     "技术频道_抖音号",
		CanonicalURL: "https://www.douyin.com/video/7388001001",
		AvatarThumb:  histAvDy,
		CoverThumb:   histCvDy,
		DurationText: "01:35",
	})
	_ = b.HistoryStore().Record(storage.HistoryEntry{
		Site:         "bilibili",
		Kind:         "user",
		ID:           "20002",
		Title:        "独立游戏工坊",
		Subtitle:     "每周开发日志与渲染管线直播",
		CanonicalURL: "https://space.bilibili.com/20002",
		AvatarThumb:  histAv2,
	})
	_ = b.HistoryStore().Record(storage.HistoryEntry{
		Site:         "bilibili",
		Kind:         "video",
		ID:           "BV1xx411c7mD",
		Title:        "示例本地缓存视频：4K HDR 与 Hi-Res 封装验证",
		Subtitle:     "技术频道_Alpha",
		CanonicalURL: "https://www.bilibili.com/video/BV1xx411c7mD",
		AvatarThumb:  histAv1,
		CoverThumb:   histCv1,
		DurationText: "02:00",
	})

	fixtureBackend := &screenshotBackend{
		BackendAPI: b,
		dyStatus: credential.Status{
			Site:     "douyin",
			LoggedIn: true,
			State:    "logged_in",
			Source:   "cookie",
			UID:      "900086",
			SecUID:   "MS4wLjABAAAA_uishots_self_demo",
			UniqueID: "trace_douyin_demo",
			Uname:    "TRACE抖音演示账号",
		},
		schedulers: map[string]service.SiteSchedulerStatus{
			"bilibili": {
				Site:        "bilibili",
				DisplayName: "哔哩哔哩",
				LastCheckAt: "2026-11-12T10:00:00Z",
				NextCheckAt: "2026-11-12T11:00:00Z",
			},
			"douyin": {
				Site:            "douyin",
				DisplayName:     "抖音",
				LastCheckAt:     "2026-11-12T10:05:00Z",
				BackingOff:      true,
				CurrentInterval: "1 小时",
				BackoffUntil:    "2026-11-12T12:00:00Z",
				BackoffSummary:  "抖音：因风控放慢到 1 小时，11 月 12 日 12:00 恢复",
			},
		},
	}
	app := ui.NewApp(fixtureBackend, player.NewNullAudioSink())
	defer app.Close()

	seedRecentAndLive := func() {
		for i := range app.Subscriptions {
			if i == 0 {
				app.Subscriptions[i].LatestUpdateTs = time.Now().Add(-15 * time.Minute).Unix()
			} else if i == 1 {
				app.Subscriptions[i].LatestUpdateTs = time.Now().Add(-2 * time.Hour).Unix()
			}
		}
		app.LiveRooms = []live.RoomState{
			{
				UID:         20001,
				RoomID:      9001,
				Uname:       "技术频道_Alpha",
				Title:       "纯 Go + MyGo 原生架构实测直播",
				LiveStatus:  1,
				StatusText:  "直播中",
				Quality:     10000,
				QualityName: "原画",
				Codec:       "avc",
			},
			{
				UID:               20002,
				RoomID:            9002,
				Uname:             "独立游戏工坊",
				Title:             "渲染管线与着色器实时调试",
				LiveStatus:        1,
				StatusText:        "直播中",
				Recording:         true,
				RecordDurationSec: 185,
				RecordBytes:       48234496,
				RecordSpeedBps:    524288,
				RecordSegments:    1,
				Quality:           10000,
				QualityName:       "原画",
				Codec:             "avc",
			},
		}
		app.LiveAccounts = []service.LiveAccountState{
			{
				Site:              "douyin",
				AccountKey:        "douyin:" + douyinSecUID,
				RecordingKey:      "douyin:live:73990011",
				UID:               douyinSecUID,
				RoomID:            "73990011",
				WebRID:            "668899",
				Uname:             "技术频道_抖音号",
				Title:             "高原星空实拍 HTTP-FLV 原画直播",
				LiveStatus:        1,
				StatusText:        "直播中",
				Recording:         true,
				RecordDurationSec: 92,
				RecordBytes:       25165824,
				RecordSpeedBps:    393216,
				RecordSegments:    1,
				QualityName:       "原画",
				Codec:             "hevc",
			},
		}
		fixtureBackend.rooms = append([]live.RoomState(nil), app.LiveRooms...)
		fixtureBackend.liveAccts = append([]service.LiveAccountState(nil), app.LiveAccounts...)
	}
	seedRecentAndLive()

	injectMixedHomeFeed := func() {
		app.PopulateSyntheticFeed(5000)
		if len(app.FeedItems) > 2 {
			// Insert a Douyin entry as item #1 so Home simultaneously showcases both B 站 and 抖音 author badges.
			app.FeedItems[1] = library.FeedEntry{
				FeedKey:      savedDouyinNote.ItemKey,
				Site:         "douyin",
				Kind:         "dynamic",
				RawID:        savedDouyinNote.RawID,
				AuthorUID:    douyinSecUID,
				AuthorName:   "技术频道_抖音号",
				Title:        savedDouyinNote.Title,
				Summary:      savedDouyinNote.Text,
				CanonicalURL: savedDouyinNote.URL,
				PubTs:        app.FeedItems[0].PubTs - 120,
				Cached:       true,
				CacheStatus:  "green",
				ItemKey:      savedDouyinNote.ItemKey,
			}
		}
	}

	// Verify IME composition guard does not prematurely submit parse input on Enter.
	app.Navigate(ui.RouteParse)
	composeTester := mygoui.NewTester(app.View, 1280, 860)
	_ = composeTester.Click("链接解析输入框")
	composeTester.Compose("BV1xx411c7mD", 12)
	composeTester.Key(0, mygoui.KeyEnter)
	composeGuardPassed := app.CurrentRoute == ui.RouteParse

	scenes := []struct {
		Name            string
		Setup           func()
		RequiredLabels  []string
		ForbiddenLabels []string
		// TitleBar simulates the hidden title bar's window buttons (DEC-046 ④).
		TitleBar mygoui.TitleBar
	}{
		{
			Name: "home",
			Setup: func() {
				app.ImageViewer.Open = false
				app.PlayerOverlay.Open = false
				app.DeleteConfirm.Open = false
				app.FeedSiteFilter = "all"
				app.Navigate(ui.RouteHome)
				seedRecentAndLive()
				injectMixedHomeFeed()
				st := b.GetUIState()
				st.HomeSeen = nil
				st.Anchor.Order = "newest"
				st.Anchor.Key = app.FeedItems[12].FeedKey
				st.Anchor.PubTs = app.FeedItems[12].PubTs
				_, _ = b.UpdateUIState(st)
				app.RestartHomeSession()
			},
			RequiredLabels: []string{
				"主页站点筛选：全部",
				"主页站点筛选：B 站",
				"主页站点筛选：抖音",
				"主页有更新头像行",
				"主页作者站点：bilibili:video:SYNTH_0001",
				"主页作者站点：" + savedDouyinNote.ItemKey,
			},
		},
		{
			Name: "home-sub",
			Setup: func() {
				app.ImageViewer.Open = false
				app.PlayerOverlay.Open = false
				app.DeleteConfirm.Open = false
				app.Navigate(ui.RouteHome)
				seedRecentAndLive()
				injectMixedHomeFeed()
				st := b.GetUIState()
				st.HomeSeen = nil
				st.Anchor.Order = "newest"
				st.Anchor.Key = app.FeedItems[12].FeedKey
				st.Anchor.PubTs = app.FeedItems[12].PubTs
				_, _ = b.UpdateUIState(st)
				app.RestartHomeSession()
				app.OpenHomeUpdatedActor("技术频道_Alpha")
			},
			RequiredLabels: []string{"返回主页时间线"},
		},
		{
			Name: "subscriptions",
			Setup: func() {
				app.SubDetailOpen = false
				app.Navigate(ui.RouteSubscriptions)
				seedRecentAndLive()
			},
			RequiredLabels: []string{"选择订阅 " + primarySub.ID},
			ForbiddenLabels: []string{
				"订阅详情账号站点：bilibili:20001",
				"订阅详情账号站点：douyin:" + douyinSecUID,
				"用户页站点标记",
			},
		},
		{
			Name: "subscription-detail",
			Setup: func() {
				seedRecentAndLive()
				if primarySub.ID != "" {
					app.OpenSubscriptionDetail(primarySub)
					app.SubDetailTab = "live"
				}
			},
		},
		{
			Name: "subscription-accounts",
			Setup: func() {
				seedRecentAndLive()
				if primarySub.ID != "" {
					app.OpenSubscriptionDetail(primarySub)
					app.SubDetailTab = "accounts"
				}
			},
			RequiredLabels: []string{
				"订阅详情账号站点：bilibili:20001",
				"订阅详情账号站点：douyin:" + douyinSecUID,
			},
		},
		{
			Name: "settings-rules",
			Setup: func() {
				seedRecentAndLive()
				app.SettingsCategory = "rules"
				app.Navigate(ui.RouteSettings)
			},
			RequiredLabels: []string{"缓存规则：默认", "新建缓存规则"},
		},
		{
			Name: "settings-account",
			Setup: func() {
				seedRecentAndLive()
				app.SettingsCategory = "account"
				app.Navigate(ui.RouteSettings)
			},
			RequiredLabels: []string{
				"账号分组：哔哩哔哩",
				"账号分组：抖音",
				"重新粘贴抖音Cookie",
				"退出抖音登录",
			},
		},
		{
			Name: "settings-subscription",
			Setup: func() {
				seedRecentAndLive()
				app.SettingsCategory = "subscription"
				app.Navigate(ui.RouteSettings)
			},
			RequiredLabels: []string{"退避状态：douyin"},
		},
		{
			Name: "parse",
			Setup: func() {
				app.Navigate(ui.RouteParse)
				app.HistoryItems = b.ListHistory()
				app.ParsePageInput = "https://www.douyin.com/video/7388001001"
				app.HistoryItemMenuID = "video:BV1xx411c7mD"
			},
			RequiredLabels: []string{
				"最近解析更多",
				"历史打开：BV1xx411c7mD",
				"历史更多：BV1xx411c7mD",
				"历史站点：bilibili:BV1xx411c7mD",
				"历史站点：douyin:7388001001",
			},
		},
		{
			Name: "video",
			Setup: func() {
				app.CurrentLibraryDetail = nil
				app.CurrentVideoItemKey = ""
				app.CurrentVideo = &bilibili.VideoDetail{
					BVID:         "BV1xx411c7mD",
					AID:          10001,
					Title:        "示例多 P 视频详情：纯 Go 桌面架构全链路解析",
					TName:        "科技",
					Desc:         "演示分 P 列表勾选、最高画质/无损音轨缓存入队与封面原图查看。",
					Cover:        "https://i0.hdslb.com/bfs/archive/cover.jpg",
					PubTs:        1710000000,
					Author:       bilibili.AuthorInfo{Mid: 20001, Name: "技术频道_Alpha"},
					Stat:         bilibili.VideoStat{View: 128000, Danmaku: 3420, Like: 9600, Coin: 4800, Favorite: 6200, Share: 1100, Reply: 850},
					CanonicalURL: "https://www.bilibili.com/video/BV1xx411c7mD",
					Pages: []bilibili.VideoPage{
						{CID: 101, Page: 1, Title: "第一部分：三栏自适应布局", Duration: 180},
						{CID: 102, Page: 2, Title: "第二部分：fMP4 无重编码混流", Duration: 240},
					},
				}
				app.SelectedPartPages = map[int]bool{1: true, 2: true}
				app.CurrentRoute = ui.RouteVideo
			},
			RequiredLabels: []string{"视频详情站点标记"},
		},
		{
			Name: "cached-video",
			Setup: func() {
				app.OpenLibraryItemDetail(savedPartialItem)
			},
			RequiredLabels: []string{"选择分P 2"},
		},
		{
			Name: "cached-dynamic",
			Setup: func() {
				app.OpenLibraryItemDetail(savedDynamicItem)
			},
			RequiredLabels: []string{"查看图片 1", "播放视频卡片：" + savedItem.ItemKey},
		},
		{
			Name: "recording-detail",
			Setup: func() {
				app.OpenLibraryItemDetail(savedLiveItem)
			},
			RequiredLabels: []string{"直播录像详情", "播放录像片段：" + savedLiveItem.ItemKey + ":P01_第一集.mp4"},
		},
		{
			Name: "user",
			Setup: func() {
				app.CurrentUser = &bilibili.UserSpace{
					Mid:          20001,
					Name:         "技术频道_Alpha",
					Level:        6,
					Fans:         58200,
					Friend:       128,
					ArchiveCount: 96,
					Sign:         "专注系统编程与多媒体工程",
					CanonicalURL: "https://space.bilibili.com/20001",
				}
				dyns := make([]bilibili.DynamicItem, 500)
				for i := 0; i < 500; i++ {
					var imgs []bilibili.DynamicMedia
					if i == 0 {
						imgs = []bilibili.DynamicMedia{
							{URL: "https://i0.hdslb.com/bfs/album/sample1.jpg", Width: 1920, Height: 1080},
							{URL: "https://i0.hdslb.com/bfs/album/sample2.jpg", Width: 1920, Height: 1080},
							{URL: "https://i0.hdslb.com/bfs/album/sample3.jpg", Width: 1920, Height: 1080},
						}
					}
					dyns[i] = bilibili.DynamicItem{
						ID:        fmt.Sprintf("dyn_%04d", i+1),
						Type:      "DYNAMIC_TYPE_DRAW",
						TypeLabel: "图文动态",
						Title:     fmt.Sprintf("创作者动态 #%03d", i+1),
						Text:      "验证 500 条空间动态虚拟列表与翻页错误保留已加载内容。",
						Images:    imgs,
					}
				}
				app.UserDynamics = dyns
				app.UserDynamicsError = "加载下一页失败：bilibili [risk_control code=-352]: 请求触发风控拦截（已保留第 1 页内容，可点击重试）"
				app.UserTab = "dynamics"
				app.CurrentRoute = ui.RouteUser
				app.PostMenuKey = "dyn:dyn_0001"
			},
			RequiredLabels: []string{"用户页站点标记", "空间更多", "帖子更多：dyn:dyn_0001", "UP主动态虚拟列表", "用户页筛选：全部", "用户页筛选：未缓存"},
		},
		{
			Name: "douyin-user",
			Setup: func() {
				app.Opening = nil
				app.LiveAccounts = []service.LiveAccountState{
					{
						Site:        "douyin",
						AccountKey:  "douyin:" + douyinSecUID,
						UID:         douyinSecUID,
						RoomID:      "73990011",
						WebRID:      "668899",
						Uname:       "技术频道_抖音号",
						Title:       "高原星空实拍 HTTP-FLV 原画直播",
						LiveStatus:  1,
						StatusText:  "直播中",
						QualityName: "原画",
						Codec:       "hevc",
					},
				}
				app.CurrentDouyinUser = &service.DouyinUserView{
					SecUID:         douyinSecUID,
					UID:            "8800101",
					UniqueID:       "alpha_douyin",
					Nickname:       "技术频道_抖音号",
					Signature:      "系统编程与高码率竖屏风光摄影记录",
					AwemeCount:     48,
					FollowerCount:  186000,
					FollowingCount: 64,
					TotalFavorited: 1420000,
					LiveStatus:     1,
					RoomID:         "73990011",
					WebRID:         "668899",
					CanonicalURL:   "https://www.douyin.com/user/" + douyinSecUID,
				}
				app.CurrentDouyinWorks = []service.DouyinWorkCardDTO{
					{
						ID:           "7388001001",
						Kind:         "video",
						Title:        "竖屏高原雪山延时摄影记录",
						DurationSec:  95,
						IsTop:        true,
						CacheKey:     savedDouyinVideo.ItemKey,
						CanonicalURL: "https://www.douyin.com/video/7388001001",
					},
					{
						ID:           "7388001002",
						Kind:         "note",
						Title:        "山谷图文实况与视频混排手记",
						PageCount:    3,
						CacheKey:     savedDouyinNote.ItemKey,
						CanonicalURL: "https://www.douyin.com/note/7388001002",
					},
					{
						ID:           "7388001003",
						Kind:         "video",
						Title:        "冰川航拍无水印原片测试",
						DurationSec:  62,
						CacheKey:     "douyin:video:7388001003",
						CanonicalURL: "https://www.douyin.com/video/7388001003",
					},
				}
				app.DouyinWorksHasMore = false
				app.DouyinWorksError = ""
				app.DouyinWorksAuthExpired = false
				app.DouyinCardMenuID = "7388001001"
				app.CurrentRoute = ui.RouteDouyinUser
			},
			RequiredLabels: []string{
				"用户页站点标记",
				"抖音主页直播横幅",
				"抖音主页开始录制",
				"抖音置顶标记：7388001001",
				"抖音图文标记：7388001002",
				"作品更多：7388001001",
				"抖音作品虚拟列表",
			},
			ForbiddenLabels: []string{"UP主动态虚拟列表", "UP主投稿虚拟列表"},
		},
		{
			Name: "douyin-video",
			Setup: func() {
				app.Opening = nil
				app.CurrentLibraryDetail = nil
				app.CurrentDouyinWork = &service.DouyinWorkView{
					ID:           "7388001001",
					Kind:         "video",
					Title:        "竖屏高原雪山延时摄影记录",
					Desc:         "竖屏高原雪山延时摄影记录\n无水印高码率原片归档 #延时摄影 #风光",
					Hashtags:     []string{"延时摄影", "风光"},
					CreateTime:   1710003000,
					DurationSec:  95,
					Author:       service.DouyinAuthorDTO{SecUID: douyinSecUID, UniqueID: "alpha_douyin", Nickname: "技术频道_抖音号"},
					QualityLabel: "1080P",
					VideoCodec:   "hevc",
					Width:        1080,
					Height:       1440,
					Music:        &service.DouyinMusicDTO{Title: "高原风声", Author: "技术频道_抖音号", IsOriginalSound: true},
					Stats:        service.DouyinStatsDTO{DiggCount: 18200, CommentCount: 640, CollectCount: 3100, ShareCount: 420},
					CanonicalURL: "https://www.douyin.com/video/7388001001",
					CacheKey:     savedDouyinVideo.ItemKey,
					CachedItem:   &savedDouyinVideo,
				}
				app.CurrentRoute = ui.RouteDouyinWork
			},
			RequiredLabels:  []string{"抖音作品作者站点标记", "缓存视频", "抖音视频封面播放"},
			ForbiddenLabels: []string{"升级缓存", "补存弹幕和字幕"},
		},
		{
			Name: "cached-douyin-video",
			Setup: func() {
				app.OpenLibraryItemDetail(savedDouyinVideo)
			},
			RequiredLabels:  []string{"抖音作品作者站点标记", "缓存视频", "抖音视频封面播放"},
			ForbiddenLabels: []string{"升级缓存", "补存弹幕和字幕", "抖音作品统计行"},
		},
		{
			Name: "douyin-note",
			Setup: func() {
				app.OpenLibraryItemDetail(savedDouyinNote)
			},
			RequiredLabels: []string{
				"抖音作品作者站点标记",
				"缓存图文",
				"查看图文页 1",
				"实况标记 2",
				"查看图文页 3",
				"视频页标记 3",
				"配乐栏：douyin-note:7388001002",
				"配乐播放暂停：douyin-note:7388001002",
				"配乐进度条：douyin-note:7388001002",
			},
		},
		{
			Name: "library",
			Setup: func() {
				app.LibrarySiteFilter = "all"
				app.Navigate(ui.RouteLibrary)
				app.ExpandedItemKey = savedDynamicItem.ItemKey
			},
			RequiredLabels: []string{
				"媒体库站点筛选：全部",
				"媒体库站点筛选：B 站",
				"媒体库站点筛选：抖音",
				"媒体更多：" + savedDynamicItem.ItemKey,
				"媒体库站点：" + savedDynamicItem.ItemKey,
				"媒体库站点：" + savedDouyinVideo.ItemKey,
			},
		},
		{
			Name: "library-delete",
			Setup: func() {
				app.Navigate(ui.RouteLibrary)
				app.ExpandedItemKey = ""
				plan, _ := b.PlanDelete(savedItem.ItemKey)
				app.DeleteConfirm = ui.DeleteConfirmState{
					Open:        true,
					Item:        savedItem,
					Plan:        plan,
					FileCount:   len(plan.RegisteredFiles),
					TotalBytes:  savedItem.TotalBytes,
					DeleteFiles: true,
				}
			},
		},
		{
			Name: "tasks",
			Setup: func() {
				app.DeleteConfirm.Open = false
				app.Navigate(ui.RouteTasks)
				seedRecentAndLive()
				app.Tasks = []download.Task{
					{
						TaskID:          "task_demo_dy",
						Site:            "douyin",
						Kind:            "video",
						TargetID:        "7388001001",
						Title:           "正在缓存：竖屏高原雪山延时摄影记录",
						Status:          "running",
						QualityHint:     "1080P · HEVC",
						BytesDownloaded: 5242880,
						TotalBytes:      8388608,
						Progress:        0.62,
					},
					{
						TaskID:          "task_demo_run",
						Site:            "bilibili",
						Kind:            "video",
						TargetID:        "BV1run411c7mA",
						Title:           "正在缓存：渲染管线深度剖析与实测",
						Status:          "running",
						QualityHint:     "4K 超清 (120) · AVC + Hi-Res 无损 (30251)",
						BytesDownloaded: 3774873,
						TotalBytes:      8388608,
						Progress:        0.45,
					},
					{
						TaskID:          "task_demo_fail",
						Site:            "bilibili",
						Kind:            "video",
						TargetID:        "BV1yy411c7mE",
						Title:           "示例分段失败任务（带原因与移除确认）",
						Status:          "failed",
						ErrorKind:       "network",
						ErrorMessage:    "远端分段读取超时，已保留 .part 断点文件可重试",
						LastError:       "远端分段读取超时，已保留 .part 断点文件可重试",
						BytesDownloaded: 2097152,
						TotalBytes:      8388608,
						Progress:        0.25,
					},
				}
				app.TaskRemoveConfirmID = "task_demo_fail"
				app.ExpandedTaskID = ""
			},
			RequiredLabels: []string{
				"录制站点：bilibili:9002",
				"录制站点：douyin:" + douyinSecUID,
				"任务站点：task_demo_dy",
				"任务站点：task_demo_run",
			},
		},
		{
			Name: "settings",
			Setup: func() {
				app.Navigate(ui.RouteSettings)
				app.SettingsCategory = "storage"
			},
		},
		{
			Name: "settings-network",
			Setup: func() {
				app.Navigate(ui.RouteSettings)
				app.SettingsCategory = "network"
			},
			RequiredLabels: []string{"代理方式选择", "跟随系统代理说明"},
		},
		{
			Name: "settings-logs",
			Setup: func() {
				app.Navigate(ui.RouteSettings)
				app.SettingsCategory = "logs"
			},
		},
		{
			Name: "login",
			Setup: func() {
				app.LoginSite = "bilibili"
				app.Navigate(ui.RouteLogin)
				app.SavedRoute = ui.RouteVideo
				app.QRBitmap = mygoui.NewBitmap(bilibili.RenderQRRGBA("https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=demo", 6))
				app.QRStatusText = "等待扫码（86101）"
			},
			RequiredLabels: []string{"登录站点：哔哩哔哩", "登录站点：抖音"},
		},
		{
			Name: "login-douyin",
			Setup: func() {
				app.LoginSite = "douyin"
				app.DouyinCookieInput = ""
				app.Navigate(ui.RouteLogin)
			},
			RequiredLabels: []string{
				"登录站点：哔哩哔哩",
				"登录站点：抖音",
				"抖音已登录摘要",
				"抖音Cookie输入框",
				"验证并登录抖音",
			},
			ForbiddenLabels: []string{"刷新登录二维码"},
		},
		{
			Name: "image-viewer",
			Setup: func() {
				app.Navigate(ui.RouteHome)
				app.OpenItemImageViewer(savedItem.ItemKey, []string{"https://i0.hdslb.com/bfs/archive/sample.jpg"}, []string{"cover.png"}, 0)
			},
		},
		{
			Name: "image-viewer-live",
			Setup: func() {
				app.Navigate(ui.RouteHome)
				app.OpenItemImageViewer(savedDynamicItem.ItemKey, nil, []string{"image-1.png", "image-2.png", "image-3.png"}, 0)
			},
			RequiredLabels: []string{"看图播放实况", "关闭看图"},
		},
		{
			// Immersive title bar (DEC-046 ④): window buttons over the page's top-right.
			Name: "library-titlebar",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteLibrary)
			},
			TitleBar:       mygoui.TitleBar{Height: 32, Right: 138},
			RequiredLabels: []string{"窗口拖动区", "页面顶部栏", "快速解析输入框"},
		},
		{
			Name: "player",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteLibrary)
				app.OpenPlayerOverlay(savedItem)
			},
		},
		{
			Name: "player-missing-decoder",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteLibrary)
				app.OpenPlayerOverlay(savedItem)
				app.PlayerOverlay.DecoderMissing = true
				app.PlayerOverlay.MissingReason = "系统缺少 AV1 (av01) 解码器扩展，请点击下方按钮使用系统播放器打开。"
			},
		},
		{
			Name: "player-options",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteLibrary)
				app.OpenPlayerOverlay(savedItem)
				app.PlayerOverlay.MoreMenuOpen = true
			},
			RequiredLabels: []string{"弹幕开关", "弹幕透明度", "弹幕显示区域", "弹幕密度选择", "字幕语言选择", "用系统播放器打开"},
		},
		{
			Name: "player-volume",
			Setup: func() {
				app.ImageViewer.Open = false
				app.Navigate(ui.RouteLibrary)
				app.OpenPlayerOverlay(savedItem)
				app.PlayerOverlay.VolumeMenuOpen = true
			},
			RequiredLabels: []string{"播放器音量调节", "播放器静音"},
		},
		{
			// "设置 › 关于" of a release build that updates itself.
			Name: "settings-about",
			Setup: func() {
				app.PlayerOverlay.Open = false
				app.PlayerOverlay.VolumeMenuOpen = false
				app.BindUpdater(&shotUpdater{st: update.State{
					Enabled: true, Channel: "stable", CurrentVersion: "0.1.1", Phase: update.PhaseIdle,
					LastCheck: time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC), Message: update.MsgUpToDate,
					Prefs: update.Prefs{AutoCheck: true, AutoDownload: true},
				}})
				app.Navigate(ui.RouteSettings)
				app.SettingsCategory = "about"
			},
			RequiredLabels: []string{"当前版本", "提交与构建时间", "检查更新按钮", "自动检查更新开关", "自动下载更新开关"},
		},
		{
			// The line below the top bar once an update is ready, its
			// notes open.
			Name: "update-ready",
			Setup: func() {
				app.BindUpdater(&shotUpdater{st: update.State{
					Enabled: true, Channel: "stable", CurrentVersion: "0.1.1", Phase: update.PhaseReady,
					Release: &update.Release{Version: "0.1.2", Notes: "- 订阅支持合集\n- 修复直播录制在断线后的分段"},
					Prefs:   update.Prefs{AutoCheck: true, AutoDownload: true},
				}})
				app.UpdateNotesOpen = true
				app.Navigate(ui.RouteHome)
			},
			RequiredLabels: []string{"更新提示", "更新内容", "关闭更新提示"},
		},
		{
			// Preview of the green accent (Q-011); the last scene, so the
			// others keep the default blue.
			Name: "home-green-accent",
			Setup: func() {
				app.BindUpdater(nil)
				app.UpdateNotesOpen = false
				app.PlayerOverlay.Open = false
				app.PlayerOverlay.VolumeMenuOpen = false
				ui.UseAccent("green")
				app.Navigate(ui.RouteHome)
			},
			RequiredLabels: []string{"左栏导航", "中栏内容"},
		},
	}

	widths := []int{1280, 1000, 720}
	var assertions []SceneAssertion
	allPassed := composeGuardPassed
	siteFiltersOK := true
	siteBadgesOK := true
	subGridNoBadgeOK := true

	for _, sc := range scenes {
		for _, w := range widths {
			sc.Setup()
			tt := mygoui.NewTester(app.View, w, 860)
			if sc.TitleBar.Height > 0 {
				tt.SetTitleBar(sc.TitleBar)
			}
			tt.Frame()

			leftRect, okL := tt.Find("左栏导航")
			centerRect, okC := tt.Find("中栏内容")
			var panelPtr *mygoui.Rect
			noOverflow := okL && okC && leftRect.X >= -0.5 && (centerRect.X+centerRect.W) <= float32(w)+0.5
			controlsVisible := true
			var missingLabels []string
			for _, label := range sc.RequiredLabels {
				r, ok := tt.Find(label)
				if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.Y < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > 860.5 {
					controlsVisible = false
					missingLabels = append(missingLabels, fmt.Sprintf("%s(ok=%v,rect=%.1f,%.1f,%.1f,%.1f)", label, ok, r.X, r.Y, r.W, r.H))
				}
			}
			forbiddenAbsent := true
			for _, label := range sc.ForbiddenLabels {
				if _, ok := tt.Find(label); ok {
					forbiddenAbsent = false
				}
			}

			if sc.Name == "home" || sc.Name == "library" {
				if !controlsVisible {
					siteFiltersOK = false
				}
			}
			switch sc.Name {
			case "home", "subscription-accounts", "parse", "user", "douyin-user", "library", "tasks":
				if !controlsVisible {
					siteBadgesOK = false
				}
			case "subscriptions":
				if !forbiddenAbsent {
					subGridNoBadgeOK = false
				}
			}

			// The context panel, where the page has one, sits between
			// the rail and the page (DEC-039).
			if pRect, okP := tt.Find("上下文侧栏"); okP {
				pc := pRect
				panelPtr = &pc
				if pRect.X < leftRect.X+leftRect.W-0.5 || pRect.X+pRect.W > centerRect.X+0.5 {
					noOverflow = false
				}
			}

			img := tt.Image()

			// Verify sidebars remain fixed when scrolling Center column (NFR-UI-001).
			tt.Scroll(centerRect.X+centerRect.W/2, centerRect.Y+300, 0, -320)
			leftAfter, _ := tt.Find("左栏导航")
			fixedSidebars := leftAfter == leftRect
			if panelPtr != nil {
				panelAfter, _ := tt.Find("上下文侧栏")
				if panelAfter != *panelPtr {
					fixedSidebars = false
				}
			}

			fileName := fmt.Sprintf("%s-%dx860.png", sc.Name, w)
			var pngBuf bytes.Buffer
			if err := png.Encode(&pngBuf, img); err != nil {
				return err
			}
			if err := storage.WriteFileAtomic(filepath.Join(outDir, fileName), pngBuf.Bytes(), 0o644); err != nil {
				return err
			}

			scenePassed := noOverflow && fixedSidebars && controlsVisible && forbiddenAbsent
			if !scenePassed {
				allPassed = false
			}
			assertions = append(assertions, SceneAssertion{
				Scene:                   sc.Name,
				Width:                   w,
				Height:                  860,
				Screenshot:              fileName,
				LeftRect:                leftRect,
				CenterRect:              centerRect,
				PanelRect:               panelPtr,
				NoHorizontalOver:        noOverflow,
				FixedSidebarsOnScr:      fixedSidebars,
				RequiredControlsVisible: controlsVisible,
				ForbiddenControlsAbsent: forbiddenAbsent,
				RequiredChecked:         sc.RequiredLabels,
				MissingLabels:           missingLabels,
				ForbiddenChecked:        sc.ForbiddenLabels,
				Passed:                  scenePassed,
			})
		}
	}

	if !siteFiltersOK || !siteBadgesOK || !subGridNoBadgeOK {
		allPassed = false
	}

	report := UIShotsReport{
		GeneratedAt:                 time.Now().UTC().Format(time.RFC3339),
		HeadlessTester:              true,
		AudioMutedNullSink:          true,
		VirtualFeedCount:            5000,
		ComposeGuardPassed:          composeGuardPassed,
		SiteFiltersVerified:         siteFiltersOK,
		SiteBadgesInSixPlaces:       siteBadgesOK,
		SubscriptionGridNoSiteBadge: subGridNoBadgeOK,
		Assertions:                  assertions,
		Passed:                      allPassed,
	}

	rawJSON, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := storage.WriteFileAtomic(filepath.Join(outDir, "assertions.json"), rawJSON, 0o644); err != nil {
		return err
	}
	if !allPassed {
		return fmt.Errorf("one or more UI assertions failed")
	}
	fmt.Printf("uishots: generated %d screenshots in %s (all passed)\n", len(assertions), outDir)
	return nil
}

// shotUpdater shows a fixed state of the updates.
type shotUpdater struct{ st update.State }

func (u *shotUpdater) State() update.State      { return u.st }
func (u *shotUpdater) CheckNow(context.Context) {}
func (u *shotUpdater) Download(context.Context) {}
func (u *shotUpdater) DismissBanner()           {}
func (u *shotUpdater) DismissUpdatedNotice()    {}
func (u *shotUpdater) RestartPrompt() string    { return update.RestartPrompt(1, 0) }
func (u *shotUpdater) Restart()                 {}
func (u *shotUpdater) PrefsChanged()            {}
