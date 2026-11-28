package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/ui"
	"github.com/chialecode/trace/internal/update"
)

// Screenshot-only live and multi-site states depict logged-in accounts, per-site backoff,
// and active recordings without starting background workers or real network requests.
type screenshotBackend struct {
	service.BackendAPI
	rooms           []live.RoomState
	dyStatus        credential.Status
	wbStatus        credential.Status
	xStatus         credential.Status
	schedulers      map[string]service.SiteSchedulerStatus
	liveAccts       []service.LiveAccountState
	queue           []service.CacheQueueTask
	conflicts       []service.CacheConflictDTO
	updateHist      *update.History
	installedDevVer string
	// 我的 fixtures (mine_fixture.go): lists come from memory, never the network.
	mineDouyinOut  bool
	mineErr        map[string]error // site/kind
	mineMediaPaths map[string]string
	// 远程 / 导入导出 fixtures (remote_fixture.go).
	remote remoteFixture
}

func (b *screenshotBackend) MediaPath(itemKey, relPath string) (string, error) {
	if b.mineMediaPaths != nil {
		if p, ok := b.mineMediaPaths[itemKey+"::"+relPath]; ok && p != "" {
			return p, nil
		}
	}
	return b.BackendAPI.MediaPath(itemKey, relPath)
}

func (b *screenshotBackend) GetUpdateHistory() update.History {
	if b.updateHist != nil {
		return update.History{
			Version: b.updateHist.Version,
			Checks:  append([]update.CheckRecord(nil), b.updateHist.Checks...),
			Updates: append([]update.UpdateRecord(nil), b.updateHist.Updates...),
		}
	}
	return b.BackendAPI.GetUpdateHistory()
}

func (b *screenshotBackend) InstalledDevVersion() string {
	if b.installedDevVer != "" {
		return b.installedDevVer
	}
	return b.BackendAPI.InstalledDevVersion()
}

func (b *screenshotBackend) ListCacheQueue() []service.CacheQueueTask {
	if len(b.queue) > 0 {
		return append([]service.CacheQueueTask(nil), b.queue...)
	}
	return b.BackendAPI.ListCacheQueue()
}

func (b *screenshotBackend) ListCacheConflicts() ([]service.CacheConflictDTO, error) {
	if len(b.conflicts) > 0 {
		return append([]service.CacheConflictDTO(nil), b.conflicts...), nil
	}
	return b.BackendAPI.ListCacheConflicts()
}

func (b *screenshotBackend) PendingCacheConflictCount() int {
	if len(b.conflicts) > 0 {
		n := 0
		for _, c := range b.conflicts {
			if c.State != service.ConflictStateResolving {
				n++
			}
		}
		return n
	}
	return b.BackendAPI.PendingCacheConflictCount()
}

func (b *screenshotBackend) InspectCacheConflictDiff(_ context.Context, id string) (service.CacheConflictDiffDTO, error) {
	for _, c := range b.conflicts {
		if c.ID == id {
			_, locTitle, remTitle := service.DiffTextSideBySide("合成演示视频：V1 初版投稿", "合成演示视频：V2 重制高清版")
			_, locText, remText := service.DiffTextSideBySide("初版说明：1080P 单分P，含 1 张附带说明图。", "修订说明：4K HDR 双分P，补充勘误片段与新版配图。")
			return service.CacheConflictDiffDTO{
				Conflict: c,
				Local: service.ConflictDiffSideDTO{
					Heading: "本地已缓存（旧版 V1）",
					Summary: c.LocalSummary,
					Title:   "合成演示视频：V1 初版投稿",
					Text:    "初版说明：1080P 单分P，含 1 张附带说明图。",
				},
				Remote: service.ConflictDiffSideDTO{
					Heading: "源站当前（新版 V2）",
					Summary: c.RemoteSummary,
					Title:   "合成演示视频：V2 重制高清版",
					Text:    "修订说明：4K HDR 双分P，补充勘误片段与新版配图。",
				},
				TitleChanged: true,
				LocalTitle:   locTitle,
				RemoteTitle:  remTitle,
				TextChanged:  true,
				LocalText:    locText,
				RemoteText:   remText,
				LocalLinks: []service.TextLinkDTO{
					{Label: "摄影超话", URL: "https://weibo.com/p/100808synth01"},
				},
				RemoteLinks: []service.TextLinkDTO{
					{Label: "摄影超话", URL: "https://weibo.com/p/100808synth01"},
					{Label: "勘误说明", URL: "https://example.com/errata"},
				},
				MediaPairs: []service.ConflictMediaPairDTO{
					{
						Index:      1,
						Kind:       "video",
						Status:     service.ConflictDiffStatusChanged,
						StatusText: "已变更",
						Local:      &service.ConflictMediaSideDTO{Kind: "video", Label: "P01 初版正片", Sublabel: "1080P · AVC · 03:20 · CID 101", LocalItemKey: c.ItemKey, LocalRelPath: "cover.jpg", PlayRelPath: "P01.mp4", DurationSec: 200, CID: 101},
						Remote:     &service.ConflictMediaSideDTO{Kind: "video", Label: "P01 重制版", Sublabel: "4K · HEVC · 03:35 · CID 209", DurationSec: 215, CID: 209},
					},
					{
						Index:      2,
						Kind:       "video",
						Status:     service.ConflictDiffStatusRemoteOnly,
						StatusText: "源站新增",
						Remote:     &service.ConflictMediaSideDTO{Kind: "video", Label: "P02 勘误补充", Sublabel: "1080P · 01:10 · CID 210", DurationSec: 70, CID: 210},
					},
					{
						Index:      3,
						Kind:       "image",
						Status:     service.ConflictDiffStatusSame,
						StatusText: "相同",
						Local:      &service.ConflictMediaSideDTO{Kind: "image", Label: "封面 / 配图 1", Sublabel: "1280×720", LocalItemKey: c.ItemKey, LocalRelPath: "cover.jpg"},
						Remote:     &service.ConflictMediaSideDTO{Kind: "image", Label: "封面 / 配图 1", Sublabel: "1280×720"},
					},
				},
			}, nil
		}
	}
	return b.BackendAPI.InspectCacheConflictDiff(context.Background(), id)
}

func (b *screenshotBackend) Statuses() map[string]credential.Status {
	bili := b.BackendAPI.StatusOf("bilibili")
	return map[string]credential.Status{
		"bilibili": bili,
		"douyin":   b.dyStatus,
		"weibo":    b.wbStatus,
		"x":        b.xStatus,
	}
}

func (b *screenshotBackend) StatusOf(siteID string) credential.Status {
	switch strings.ToLower(strings.TrimSpace(siteID)) {
	case "douyin":
		if b.mineDouyinOut {
			return credential.Status{Site: "douyin", State: "logged_out"}
		}
		return b.dyStatus
	case "weibo":
		return b.wbStatus
	case "x":
		return b.xStatus
	}
	return b.BackendAPI.StatusOf(siteID)
}

func (b *screenshotBackend) HasAnyLogin() bool {
	return b.BackendAPI.HasAnyLogin() || b.dyStatus.LoggedIn || b.wbStatus.LoggedIn || b.xStatus.LoggedIn
}

func (b *screenshotBackend) FollowingGroups(_ context.Context, siteID string) ([]service.FollowingGroupDTO, error) {
	switch strings.ToLower(strings.TrimSpace(siteID)) {
	case "weibo", "x":
		return nil, nil
	}
	return []service.FollowingGroupDTO{
		{ID: "-10", Name: "特别关注", Count: 3},
		{ID: "7", Name: "游戏", Count: 12},
	}, nil
}

func (b *screenshotBackend) FollowingCreatorsPage(_ context.Context, siteID, _ string, page int) (service.FollowingCreatorsPageDTO, error) {
	switch strings.ToLower(strings.TrimSpace(siteID)) {
	case "weibo":
		return service.FollowingCreatorsPageDTO{
			Site: "weibo",
			Page: max(1, page),
			Items: []service.FollowingCreatorDTO{
				{Site: "weibo", UID: "500000001", Name: "技术频道_微博", Sign: "系统工程与媒体工具开发笔记", Subscribed: true, OwnerSubName: "技术频道_Alpha"},
				{Site: "weibo", UID: "500000002", Name: "合成摄影日记", Sign: "胶片与风光摄影日常"},
				{Site: "weibo", UID: "500000003", Name: "合成设计周刊", Sign: "每周分享桌面界面与字体排印"},
			},
			HasMore:       false,
			GroupsEnabled: false,
		}, nil
	case "x":
		return service.FollowingCreatorsPageDTO{
			Site: "x",
			Page: max(1, page),
			Items: []service.FollowingCreatorDTO{
				{Site: "x", UID: "1000000000000000001", UniqueID: "alpha_x", Name: "技术频道_X", Sign: "系统工程与跨平台媒体工具实践", Subscribed: true, OwnerSubName: "技术频道_Alpha"},
				{Site: "x", UID: "1000000000000000004", UniqueID: "synth_x_dev", Name: "合成X开发者", Sign: "每周分享 Go 与桌面界面工程"},
				{Site: "x", UID: "1000000000000000005", UniqueID: "synth_x_photo", Name: "合成X摄影师", Sign: "胶片与风光摄影归档"},
			},
			HasMore:       false,
			GroupsEnabled: false,
		}, nil
	}
	return service.FollowingCreatorsPageDTO{
		Site: "bilibili",
		Page: max(1, page),
		Items: []service.FollowingCreatorDTO{
			{Site: "bilibili", UID: "20001", Name: "技术频道_Alpha", Sign: "系统编程与多媒体工程", Subscribed: true, OwnerSubName: "技术频道_Alpha"},
			{Site: "bilibili", UID: "30001", Name: "合成科技实验室", Sign: "每周硬件评测"},
		},
		HasMore:       false,
		GroupsEnabled: true,
	}, nil
}

func (b *screenshotBackend) FollowingCreatorsPageWithCursor(ctx context.Context, siteID, groupID, _ string) (service.FollowingCreatorsPageDTO, error) {
	return b.FollowingCreatorsPage(ctx, siteID, groupID, 1)
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

type fixtureEnv struct {
	srv               *httptest.Server
	tmpRoot           string
	rawBackend        *service.Backend
	backend           *screenshotBackend
	douyinSecUID      string
	weiboUID          string
	xUID              string
	xHandle           string
	xOnlyUID          string
	xOnlyHandle       string
	primarySub        subscription.Subscription
	auroraSub         subscription.Subscription
	xOnlySub          subscription.Subscription
	savedItem         library.ItemMetadata
	savedLiveItem     library.ItemMetadata
	savedPartialItem  library.ItemMetadata
	savedDynamicItem  library.ItemMetadata
	savedDouyinVideo  library.ItemMetadata
	savedDouyinNote   library.ItemMetadata
	savedWeiboPost    library.ItemMetadata
	savedWeiboRetweet library.ItemMetadata
	savedWeiboEdited  library.ItemMetadata
	savedXPost        library.ItemMetadata
	savedXRetweet     library.ItemMetadata
	savedXThread      library.ItemMetadata
	savedXArticle     library.ItemMetadata
}

func (e *fixtureEnv) close() {
	if e.rawBackend != nil {
		_ = e.rawBackend.Close()
	}
	if e.srv != nil {
		e.srv.Close()
	}
	if e.tmpRoot != "" {
		_ = os.RemoveAll(e.tmpRoot)
	}
}

func seedFixtures() (env *fixtureEnv, err error) {
	env = &fixtureEnv{
		douyinSecUID: "MS4wLjABAAAA_uishots_creator_01",
		weiboUID:     "500000001",
		xUID:         "1000000000000000001",
		xHandle:      "alpha_x",
		xOnlyUID:     "1000000000000000006",
		xOnlyHandle:  "synth_x_col",
	}
	defer func() {
		if err != nil {
			env.close()
			env = nil
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":10086,"uname":"TRACE演示账号","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
	})
	env.srv = httptest.NewServer(mux)

	env.tmpRoot, err = os.MkdirTemp("", "trace-uishots-*")
	if err != nil {
		return nil, err
	}

	loc := appdirs.Locations{
		Channel:         "dev",
		Mode:            "dev",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		DataDir:         filepath.Join(env.tmpRoot, "data"),
		DefaultCacheDir: filepath.Join(env.tmpRoot, "cache"),
		CredentialsDir:  filepath.Join(env.tmpRoot, "data", "credentials"),
		LogsDir:         filepath.Join(env.tmpRoot, "data", "logs"),
		LockFile:        filepath.Join(env.tmpRoot, "data", "app.lock"),
	}

	desk := service.NewMockDesktop()
	b, err := service.NewBackend(service.Options{
		Locations: loc,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      env.srv.URL,
				PassportBase: env.srv.URL,
				WWWBase:      env.srv.URL,
				SpaceBase:    env.srv.URL,
				DynamicBase:  env.srv.URL,
				LiveAPIBase:  env.srv.URL,
				LiveWebBase:  env.srv.URL,
			},
			PacingInterval: time.Millisecond,
			AllowLocalhost: true,
		},
		Desktop:          desk,
		DisableBgWorkers: true,
	})
	if err != nil {
		return nil, err
	}
	env.rawBackend = b

	// Seed synthetic Bilibili + Douyin data
	_, _ = b.ImportCookie(context.Background(), "SESSDATA=synth_sess; bili_jct=synth_csrf; DedeUserID=10086", "synth_refresh")
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
				UID:      env.douyinSecUID,
				UniqueID: "alpha_douyin",
				WebRID:   "668899",
				Name:     "技术频道_抖音号",
			})
			if linkErr == nil {
				env.primarySub = linked
			} else {
				env.primarySub = sub
			}
			if wbLinked, wbErr := b.LinkSubscriptionAccountRef(env.primarySub.ID, subscription.AccountRef{
				Site: "weibo",
				UID:  env.weiboUID,
				Name: "技术频道_微博",
			}); wbErr == nil {
				env.primarySub = wbLinked
			}
			if xLinked, xErr := b.LinkSubscriptionAccountRef(env.primarySub.ID, subscription.AccountRef{
				Site:     "x",
				UID:      env.xUID,
				UniqueID: env.xHandle,
				Name:     "技术频道_X",
			}); xErr == nil {
				env.primarySub = xLinked
			}
			if withLinks, lErr := b.SetSubscriptionLinks(env.primarySub.ID, []subscription.CustomLink{
				{URL: "https://link3.cc/alpha_tech"},
				{Label: "创作手记", URL: "https://linktr.ee/alpha_notes"},
			}); lErr == nil {
				env.primarySub = withLinks
			}
		}
	}
	env.auroraSub, _ = b.CreateSubscription("极光纪实", "高原延时与图文手记", []subscription.AccountRef{
		{
			Site:     "douyin",
			UID:      "MS4wLjABAAAA_uishots_creator_02",
			UniqueID: "aurora_doc",
			WebRID:   "778899",
			Name:     "极光纪实",
		},
	}, subscription.SubRules{ScheduledCheck: true})
	env.xOnlySub, _ = b.CreateSubscription("X 合成专栏", "仅关联 X 账号，用于校验无直播状态与无录制按钮", []subscription.AccountRef{
		{
			Site:     "x",
			UID:      env.xOnlyUID,
			UniqueID: env.xOnlyHandle,
			Name:     "X 合成专栏",
		},
	}, subscription.EmptySubRules())

	if env.primarySub.ID != "" {
		if updated, rErr := b.SetSubscriptionTags(env.primarySub.ID, []string{
			subscription.TagScheduledCheck,
			subscription.TagCacheVideo,
			subscription.TagCacheDraw,
		}); rErr == nil {
			env.primarySub = updated
		}
	}
	if env.auroraSub.ID != "" {
		if updated, rErr := b.SetSubscriptionTags(env.auroraSub.ID, []string{
			subscription.TagScheduledCheck,
			subscription.TagCacheVideo,
			subscription.TagCacheDraw,
		}); rErr == nil {
			env.auroraSub = updated
		}
	}
	if subsList, lErr := b.ListSubscriptions(); lErr == nil {
		for _, s := range subsList {
			if s.Mid == 20003 {
				_, _ = b.SetSubscriptionTags(s.ID, []string{
					subscription.TagScheduledCheck,
					subscription.TagCacheVideo,
				})
			}
		}
	}

	itemDir := filepath.Join(loc.DefaultCacheDir, "bilibili", "20001_技术频道_Alpha", "2026-03-01_video_BV1xx411c7mD_示例缓存视频")
	_ = os.MkdirAll(itemDir, 0o700)
	vBytes, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		return nil, err
	}
	aBytes, err := download.BuildSyntheticAudioM4S(15)
	if err != nil {
		return nil, err
	}
	vPath := filepath.Join(itemDir, "v.m4s")
	aPath := filepath.Join(itemDir, "a.m4s")
	mp4Path := filepath.Join(itemDir, "P01_第一集.mp4")
	_ = os.WriteFile(vPath, vBytes, 0o600)
	_ = os.WriteFile(aPath, aBytes, 0o600)
	if _, err := download.MuxDASHFiles(vPath, aPath, mp4Path); err != nil {
		return nil, err
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

	env.savedItem, _ = b.LibraryStore().SaveItem(itemDir, library.ItemMetadata{
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
		return nil, err
	}

	liveItemDir := filepath.Join(loc.DefaultCacheDir, "bilibili", "20001_技术频道_Alpha", "2026-03-01_live_9001_示例直播录像")
	_ = os.MkdirAll(liveItemDir, 0o700)
	_ = os.WriteFile(filepath.Join(liveItemDir, "P01_第一集.mp4"), mp4Raw, 0o600)
	_ = os.WriteFile(filepath.Join(liveItemDir, "cover.png"), coverBuf.Bytes(), 0o600)
	env.savedLiveItem, _ = b.LibraryStore().SaveItem(liveItemDir, library.ItemMetadata{
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
			RoomID:      9001,
			Quality:     10000,
			QualityName: "原画",
			Codec:       "avc",
			StartedAt:   time.Now().Add(-95 * time.Minute).UTC().Format(time.RFC3339),
			EndedAt:     time.Now().Add(-93 * time.Minute).UTC().Format(time.RFC3339),
			Segments: []library.LiveSegmentInfo{
				{File: "P01_第一集.mp4", DurationSec: 120, Quality: 10000, Codec: "avc"},
			},
			Incidents: []library.LiveIncident{
				{Kind: library.LiveIncidentReconnect, At: time.Now().Add(-94 * time.Minute).UTC().Format(time.RFC3339), DurationSec: 3.5},
			},
		},
	})

	partialDir := filepath.Join(loc.DefaultCacheDir, "bilibili", "20001_技术频道_Alpha", "videos", "synthetic-partial-detail")
	if err := os.MkdirAll(partialDir, 0o700); err != nil {
		return nil, err
	}
	for name, raw := range map[string][]byte{"P01.mp4": mp4Raw, "cover.png": coverBuf.Bytes()} {
		if err := os.WriteFile(filepath.Join(partialDir, name), raw, 0o600); err != nil {
			return nil, err
		}
	}
	env.savedPartialItem, err = b.LibraryStore().SaveItem(partialDir, library.ItemMetadata{
		Site: "bilibili", Kind: "video", ID: "BV1PartialFixture", Title: "本机部分缓存视频：继续缓存缺失的分P",
		Author: library.ItemAuthor{Mid: 20001, Name: "技术频道_Alpha"}, PubTs: 1710000000, CachedAt: "2026-03-01T12:00:00Z",
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}, {Role: "cover", Path: "cover.png"}},
		Parts: []library.VideoPartRecord{{Page: 1, CID: 101, File: "P01.mp4", Title: "第一部分", Duration: 120}}, ExpectedPages: []int{1, 2},
	})
	if err != nil {
		return nil, err
	}

	dynamicDir := filepath.Join(loc.DefaultCacheDir, "bilibili", "20001_技术频道_Alpha", "dynamics", "synthetic-local-detail")
	if err := os.MkdirAll(dynamicDir, 0o700); err != nil {
		return nil, err
	}
	var dynamicImages []library.MediaFile
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("image-%d.png", i)
		if err := os.WriteFile(filepath.Join(dynamicDir, name), coverBuf.Bytes(), 0o600); err != nil {
			return nil, err
		}
		dynamicImages = append(dynamicImages, library.MediaFile{Role: "image", Path: name, Width: 320, Height: 200})
	}
	// The first image is a live photo (实况) with its motion clip (DEC-046 ①).
	if err := os.WriteFile(filepath.Join(dynamicDir, "image-1_live.mp4"), mp4Raw, 0o600); err != nil {
		return nil, err
	}
	dynamicImages[0].Motion = "image-1_live.mp4"
	dynamicImages = append(dynamicImages, library.MediaFile{Role: "motion", Path: "image-1_live.mp4", Duration: 3})
	env.savedDynamicItem, err = b.LibraryStore().SaveItem(dynamicDir, library.ItemMetadata{
		Site: "bilibili", Kind: "dynamic", ID: "synthetic-local-detail", Title: "本机动态详情：图片与引用视频",
		Text: "图片和引用视频均已缓存，点击即可在应用内查看。", PubTs: 1710004000, CachedAt: "2026-03-02T11:30:00Z",
		Author:      library.ItemAuthor{Mid: 20001, Name: "技术频道_Alpha"},
		DynamicType: "DYNAMIC_TYPE_AV", TypeLabel: "视频动态", Media: dynamicImages, Videos: []string{env.savedItem.RawID},
	})
	if err != nil {
		return nil, err
	}

	// Seed cached Douyin video and mixed Douyin note (with image + live photo + video page + pure-audio WAV BGM)
	dyVidDir := filepath.Join(loc.DefaultCacheDir, "douyin", "a1b2c3d4e5f6", "videos", "7388001001")
	if err := os.MkdirAll(dyVidDir, 0o700); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(dyVidDir, "video.mp4"), mp4Raw, 0o600)
	_ = os.WriteFile(filepath.Join(dyVidDir, "cover.png"), portraitBuf.Bytes(), 0o600)
	env.savedDouyinVideo, err = b.LibraryStore().SaveItem(dyVidDir, library.ItemMetadata{
		Version:  1,
		ID:       "7388001001",
		RawID:    "7388001001",
		Site:     "douyin",
		Kind:     "video",
		Status:   "complete",
		Title:    "竖屏高原雪山延时摄影记录",
		Desc:     "竖屏高原雪山延时摄影记录\n无水印高码率原片归档 #延时摄影 #风光",
		URL:      "https://www.douyin.com/video/7388001001",
		PubTs:    1710004200,
		CachedAt: "2026-03-02T12:00:00Z",
		Duration: 95,
		Author: library.ItemAuthor{
			Site:     "douyin",
			UID:      env.douyinSecUID,
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
		return nil, err
	}

	dyNoteDir := filepath.Join(loc.DefaultCacheDir, "douyin", "a1b2c3d4e5f6", "notes", "7388001002")
	if err := os.MkdirAll(dyNoteDir, 0o700); err != nil {
		return nil, err
	}
	wavRaw := buildSyntheticWAVBytes(48000, 2, 48000*4)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "p01.png"), portraitBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "p02.png"), portraitBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "p02_motion.mp4"), mp4Raw, 0o600)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "p03_cover.png"), portraitBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "p03_video.mp4"), mp4Raw, 0o600)
	_ = os.WriteFile(filepath.Join(dyNoteDir, "music.wav"), wavRaw, 0o600)
	env.savedDouyinNote, err = b.LibraryStore().SaveItem(dyNoteDir, library.ItemMetadata{
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
			UID:      env.douyinSecUID,
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
		return nil, err
	}

	wbPostDir := filepath.Join(loc.DefaultCacheDir, "weibo", "500000001_技术频道_微博", "posts", "5000000000000001")
	if err := os.MkdirAll(wbPostDir, 0o700); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(wbPostDir, "img_01.png"), coverBuf.Bytes(), 0o600)
	env.savedWeiboPost, err = b.LibraryStore().SaveItem(wbPostDir, library.ItemMetadata{
		Version:   1,
		ID:        "5000000000000001",
		RawID:     "5000000000000001",
		Site:      "weibo",
		Kind:      "dynamic",
		TypeLabel: "微博",
		Status:    "complete",
		Title:     "微博长文与多图实况示例",
		Text:      "微博图文归档示例：保存图片与实况静帧，详见 架构手记 与 #离线媒体库#。",
		Links: []library.ItemLink{
			{Label: "架构手记", URL: "https://weibo.com/ttarticle/p/show?id=2309405000000000000001"},
			{Label: "#离线媒体库#", URL: "https://s.weibo.com/weibo?q=%23%E7%A6%BB%E7%BA%BF%E5%AA%92%E4%BD%93%E5%BA%93%23"},
		},
		URL:      "https://weibo.com/500000001/5000000000000001",
		PubTs:    1710004500,
		CachedAt: "2026-03-02T12:00:00Z",
		Author: library.ItemAuthor{
			Site: "weibo",
			UID:  env.weiboUID,
			Name: "技术频道_微博",
		},
		Media: []library.MediaFile{
			{Role: "image", Path: "img_01.png", Width: 480, Height: 200},
		},
	})
	if err != nil {
		return nil, err
	}

	wbRetweetDir := filepath.Join(loc.DefaultCacheDir, "weibo", "500000001_技术频道_微博", "forwards", "5000000000000015")
	if err := os.MkdirAll(filepath.Join(wbRetweetDir, "images"), 0o700); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(wbRetweetDir, "images", "01.png"), coverBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(wbRetweetDir, "images", "02.png"), portraitBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(wbRetweetDir, "images", "02_live.mp4"), []byte("synth-live-motion"), 0o600)
	_ = os.WriteFile(filepath.Join(wbRetweetDir, "cover.png"), coverBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(wbRetweetDir, "video.mp4"), []byte("synth-origin-video"), 0o600)
	env.savedWeiboRetweet, err = b.LibraryStore().SaveItem(wbRetweetDir, library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:5000000000000015",
		RawID:       "5000000000000015",
		Site:        "weibo",
		Kind:        "dynamic",
		DynamicType: "forward",
		TypeLabel:   "转发微博",
		Status:      "complete",
		Title:       "转发推荐：这套多段落排印与原微博媒体归档方案非常完整。",
		Text:        "转发推荐：这套多段落排印与原微博媒体归档方案非常完整。\n全部原微博文字、图片、实况与视频均归档到订阅转发者目录，离线同样可完整回看。",
		URL:         "https://weibo.com/500000001/5000000000000015",
		PubTs:       1709980000,
		CachedAt:    "2026-03-02T12:15:00Z",
		Duration:    118,
		Author: library.ItemAuthor{
			Site: "weibo",
			UID:  env.weiboUID,
			Name: "技术频道_微博",
		},
		Via: &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      "weibo",
			UID:       env.weiboUID,
			Name:      "技术频道_微博",
			DynamicID: "5000000000000015",
		},
		Origin: &library.ItemOrigin{
			Site:         "weibo",
			ID:           "5000000000000088",
			CanonicalURL: "https://weibo.com/500000002/5000000000000088",
			Author: library.ItemAuthor{
				Site: "weibo",
				UID:  "500000002",
				Name: "合成摄影日记",
			},
			PubTs:           1709975000,
			Title:           "川西高原延时航拍成片（1080P HEVC）",
			Text:            "第一段：整理完高原风光的实况照片与单文件 MP4 视频，补齐原微博全文与媒体顺序。\n第二段：正文行高提升到 1.6 倍后，多行中文动态在桌面阅读时呼吸感明显改善。",
			VideoFromOrigin: true,
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", FromOrigin: true, Image: "images/01.png", Width: 480, Height: 200},
			{Index: 2, Kind: "motion", FromOrigin: true, Image: "images/02.png", Motion: "images/02_live.mp4", Width: 240, Height: 320, Duration: 3},
		},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.png", FromOrigin: true, Width: 480, Height: 200},
			{Role: "image", Path: "images/02.png", Motion: "images/02_live.mp4", FromOrigin: true, Width: 240, Height: 320},
			{Role: "motion", Path: "images/02_live.mp4", FromOrigin: true, Duration: 3},
			{Role: "cover", Path: "cover.png", FromOrigin: true, Width: 480, Height: 200},
			{Role: "video", Path: "video.mp4", FromOrigin: true, Duration: 118},
		},
	})
	if err != nil {
		return nil, err
	}

	wbEditedDir := filepath.Join(loc.DefaultCacheDir, "weibo", "500000001_技术频道_微博", "posts", "5000000000000018")
	if err := os.MkdirAll(filepath.Join(wbEditedDir, "images"), 0o700); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(wbEditedDir, "images", "01.png"), coverBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(wbEditedDir, "images", "edit_v01_01.png"), portraitBuf.Bytes(), 0o600)
	env.savedWeiboEdited, err = b.LibraryStore().SaveItem(wbEditedDir, library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:5000000000000018",
		RawID:       "5000000000000018",
		Site:        "weibo",
		Kind:        "dynamic",
		DynamicType: "draw",
		TypeLabel:   "图文",
		Status:      "complete",
		Title:       "已修订版本：补齐纯 Go 离线归档基准数据与实拍配图",
		Text:        "已修订版本：补齐纯 Go 离线归档基准数据与实拍配图，完整文档见 项目说明。",
		Links: []library.ItemLink{
			{Label: "项目说明", URL: "https://example.org/weibo-edit-v3"},
		},
		URL:       "https://weibo.com/500000001/5000000000000018",
		PubTs:     1709970000,
		CachedAt:  "2026-03-02T12:20:00Z",
		EditCount: 2,
		Author: library.ItemAuthor{
			Site: "weibo",
			UID:  env.weiboUID,
			Name: "技术频道_微博",
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", MediaID: "synthCurPic01", Image: "images/01.png", Width: 480, Height: 150},
		},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.png", Width: 480, Height: 150},
			{Role: "edit_image", Path: "images/edit_v01_01.png", Width: 240, Height: 320},
		},
		EditHistory: []library.ItemEditRevision{
			{
				Version:    3,
				EditTs:     1709973600,
				Text:       "已修订版本：补齐纯 Go 离线归档基准数据与实拍配图，完整文档见 项目说明。",
				Links:      []library.ItemLink{{Label: "项目说明", URL: "https://example.org/weibo-edit-v3"}},
				MediaPaths: []string{"images/01.png"},
			},
			{
				Version: 2,
				EditTs:  1709971800,
				Text:    "第二次修订：修正测试数据表格中的单位笔误。",
			},
			{
				Version:    1,
				EditTs:     1709970000,
				Text:       "原始首发版本：先发一版初稿与旧版示意图（后续编辑替换为新版配图）。",
				MediaPaths: []string{"images/edit_v01_01.png"},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	// Seed cached X original post (newest PubTs so Library top rows display all 4 sites simultaneously)
	// and cached X quote/retweet with self-reply thread and HLS-only missing notice.
	xPostDir := filepath.Join(loc.DefaultCacheDir, "x", env.xOnlyUID+"_"+env.xOnlyHandle, "posts", "1000000000000000009")
	if err := os.MkdirAll(filepath.Join(xPostDir, "images"), 0o700); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(xPostDir, "images", "01.png"), coverBuf.Bytes(), 0o600)
	env.savedXPost, err = b.LibraryStore().SaveItem(xPostDir, library.ItemMetadata{
		Version:   1,
		ID:        "1000000000000000009",
		RawID:     "1000000000000000009",
		Site:      "x",
		Kind:      "dynamic",
		TypeLabel: "帖子",
		Status:    "complete",
		Title:     "X 长文与原图混排归档示例",
		Text:      "X 帖子归档示例：完整保存 note_tweet 长帖正文、原图与静音循环 GIF，发布说明见 example.dev/release-notes，四站点凭证与限流彼此隔离。",
		Links: []library.ItemLink{
			{Label: "example.dev/release-notes", URL: "https://example.dev/release-notes"},
		},
		URL:      "https://x.com/" + env.xOnlyHandle + "/status/1000000000000000009",
		PubTs:    1710004600,
		CachedAt: "2026-03-02T12:05:00Z",
		Author: library.ItemAuthor{
			Site:     "x",
			UID:      env.xOnlyUID,
			UniqueID: env.xOnlyHandle,
			Name:     "X 合成专栏",
		},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.png", Width: 320, Height: 200},
		},
	})
	if err != nil {
		return nil, err
	}

	xRetweetDir := filepath.Join(loc.DefaultCacheDir, "x", env.xUID+"_"+env.xHandle, "forwards", "1000000000000000015")
	if err := os.MkdirAll(filepath.Join(xRetweetDir, "images"), 0o700); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(xRetweetDir, "images", "01.png"), coverBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(xRetweetDir, "images", "02.png"), portraitBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(xRetweetDir, "images", "02.mp4"), mp4Raw, 0o600)
	env.savedXRetweet, err = b.LibraryStore().SaveItem(xRetweetDir, library.ItemMetadata{
		Version:     1,
		ID:          "1000000000000000015",
		RawID:       "1000000000000000015",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "forward",
		TypeLabel:   "引用帖子",
		Status:      "partial",
		Title:       "引用推荐：X 长帖线程与原图离线归档演示",
		Text:        "引用推荐：将 X 长帖正文、作者自回复串、原图与 GIF 动图统一保存在本地媒体库，断网也能完整阅读。",
		URL:         "https://x.com/" + env.xHandle + "/status/1000000000000000015",
		PubTs:       1709979000,
		CachedAt:    "2026-03-02T12:20:00Z",
		Author: library.ItemAuthor{
			Site:     "x",
			UID:      env.xUID,
			UniqueID: env.xHandle,
			Name:     "技术频道_X",
		},
		Via: &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      "x",
			UID:       env.xUID,
			Name:      "技术频道_X",
			DynamicID: "1000000000000000015",
		},
		Origin: &library.ItemOrigin{
			Site:         "x",
			ID:           "1000000000000000088",
			CanonicalURL: "https://x.com/synth_x_dev/status/1000000000000000088",
			Author: library.ItemAuthor{
				Site:     "x",
				UID:      "1000000000000000004",
				UniqueID: "synth_x_dev",
				Name:     "合成X开发者",
			},
			PubTs: 1709974000,
			Text:  "原帖说明：采用纯标准库实现雪花 ID 校验、前端元数据运行时发现与四站点限流退避隔离。",
			Origin: &library.ItemOrigin{
				Site:         "x",
				ID:           "1000000000000000077",
				CanonicalURL: "https://x.com/synth_x_core/status/1000000000000000077",
				Author: library.ItemAuthor{
					Site:     "x",
					UID:      "1000000000000000006",
					UniqueID: "synth_x_core",
					Name:     "内核架构笔记",
				},
				PubTs: 1709970000,
				Text:  "内层引用摘要：两层引用归档限深 1 层，防止无限嵌套并保留原帖作者与正文。",
			},
		},
		InReplyTo: &library.ItemReplyTarget{
			StatusID:   "1000000000000000002",
			UserID:     "1000000000000000007",
			ScreenName: "synth_x_reviewer",
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", FromOrigin: true, Image: "images/01.png", Width: 320, Height: 200},
			{Index: 2, Kind: "video", GIF: true, FromOrigin: true, Image: "images/02.png", Video: "images/02.mp4", Width: 270, Height: 360, Duration: 3},
		},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.png", FromOrigin: true, Width: 320, Height: 200},
			{Role: "cover", Path: "images/02.png", FromOrigin: true, Width: 270, Height: 360},
			{Role: "video", Path: "images/02.mp4", GIF: true, FromOrigin: true, Duration: 3},
		},
		Thread: []library.ItemThreadPart{
			{
				ID:    "1000000000000000016",
				PubTs: 1709979300,
				Text:  "自回复补充：媒体 CDN 与 t.co 跳转全程不带 Cookie，签名参数不落盘。",
				Origin: &library.ItemOrigin{
					Site:         "x",
					ID:           "1000000000000000078",
					CanonicalURL: "https://x.com/synth_x_net/status/1000000000000000078",
					Author: library.ItemAuthor{
						Site:     "x",
						UID:      "1000000000000000008",
						UniqueID: "synth_x_net",
						Name:     "网络隔离规范",
					},
					PubTs: 1709972000,
					Text:  "线程节点引用原帖：单文件 MP4 离线回放与签名剥离不依赖在线连接。",
				},
			},
		},
		Missing: []library.MissingRecord{
			{Item: "hls_video_1", Reason: "这条视频只有分段流，暂不支持缓存"},
		},
	})
	if err != nil {
		return nil, err
	}

	xThreadDir := filepath.Join(loc.DefaultCacheDir, "x", env.xUID+"_"+env.xHandle, "posts", "1000000000000000018")
	if err := os.MkdirAll(filepath.Join(xThreadDir, "images"), 0o700); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(xThreadDir, "images", "01.png"), coverBuf.Bytes(), 0o600)
	threadPart1 := "1/3 本地信息流现在按 X 原生自回复串展示，每条推文各自保留头像、时间与链接 https://example.com/x-thread"
	threadPart2 := "2/3 左侧圆形头像之间用细竖线相连，各自图片与视频归属到对应推文，不再混成一团。"
	threadPart3 := "3/3 最后一条推文下方不画连接线，超长自回复串支持原地展开与收起。"
	env.savedXThread, err = b.LibraryStore().SaveItem(xThreadDir, library.ItemMetadata{
		Version:     1,
		ID:          "1000000000000000018",
		RawID:       "1000000000000000018",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "post",
		TypeLabel:   "图文",
		Status:      "complete",
		Title:       "1/3 本地信息流现在按 X 原生自回复串展示",
		Text:        threadPart1 + "\n\n" + threadPart2 + "\n\n" + threadPart3,
		URL:         "https://x.com/" + env.xHandle + "/status/1000000000000000018",
		PubTs:       1709978500,
		CachedAt:    "2026-03-02T12:25:00Z",
		InReplyTo: &library.ItemReplyTarget{
			StatusID:   "1000000000000000001",
			UserID:     "1000000000000000005",
			ScreenName: "synth_arch_review",
		},
		Author: library.ItemAuthor{
			Site:     "x",
			UID:      env.xUID,
			UniqueID: env.xHandle,
			Name:     "技术频道_X",
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.png", Width: 320, Height: 200},
		},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.png", Width: 320, Height: 200},
		},
		Thread: []library.ItemThreadPart{
			{
				ID:    "1000000000000000018",
				PubTs: 1709978500,
				Text:  threadPart1,
				Links: []library.ItemLink{
					{Label: "example.com/x-thread", URL: "https://example.com/x-thread"},
				},
			},
			{
				ID:           "1000000000000000019",
				PubTs:        1709978680,
				Text:         threadPart2,
				MediaIndexes: []int{1},
				Origin: &library.ItemOrigin{
					Site:         "x",
					ID:           "1000000000000000079",
					CanonicalURL: "https://x.com/synth_x_thread_ref/status/1000000000000000079",
					Author: library.ItemAuthor{
						Site:     "x",
						UID:      "1000000000000000009",
						UniqueID: "synth_x_thread_ref",
						Name:     "线程引用来源",
					},
					PubTs: 1709975000,
					Text:  "线程节点内嵌引用：每个自回复节点可独立引用其他帖子并离线保留。",
				},
			},
			{
				ID:    "1000000000000000020",
				PubTs: 1709978860,
				Text:  threadPart3,
			},
		},
	})
	if err != nil {
		return nil, err
	}

	xArticleDir := filepath.Join(loc.DefaultCacheDir, "x", env.xUID+"_"+env.xHandle, "posts", "1000000000000000029")
	if err := os.MkdirAll(filepath.Join(xArticleDir, "images"), 0o700); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(xArticleDir, "cover.png"), coverBuf.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(xArticleDir, "images", "01.png"), coverBuf.Bytes(), 0o600)
	env.savedXArticle, err = b.LibraryStore().SaveItem(xArticleDir, library.ItemMetadata{
		Version:     1,
		ID:          "1000000000000000029",
		RawID:       "1000000000000000029",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "article",
		TypeLabel:   "文章",
		Status:      "complete",
		Title:       "X Articles 长文章完整离线归档架构说明",
		Text:        "X Articles 长文章完整离线归档架构说明：支持 Draft.js 富文本排版、封面与内嵌原图离线保存、安全外链与推文引用。",
		URL:         "https://x.com/" + env.xHandle + "/status/1000000000000000029",
		PubTs:       1709978200,
		CachedAt:    "2026-03-02T12:30:00Z",
		Author: library.ItemAuthor{
			Site:     "x",
			UID:      env.xUID,
			UniqueID: env.xHandle,
			Name:     "技术频道_X",
		},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.png", Width: 320, Height: 200},
			{Role: "image", Path: "images/01.png", Width: 320, Height: 200},
		},
		Article: &library.ItemArticle{
			Title:     "X Articles 长文章完整离线归档架构说明",
			CoverRel:  "cover.png",
			PlainText: "X Articles 长文章完整离线归档架构说明：支持 Draft.js 富文本排版、封面与内嵌原图离线保存、安全外链与推文引用。",
			Blocks: []library.ItemArticleBlock{
				{Type: "header-one", Text: "一、零迁移离线存储设计"},
				{Type: "unstyled", Text: "所有封面图与内嵌图均按原图离线下载并通过二进制魔数校验，登记进本地媒体清单，阅读规格文档。", Links: []library.ItemLink{{Label: "规格文档", URL: "https://example.com/trace-article-spec"}}},
				{Type: "blockquote", Text: "清单中严禁写入远程 CDN 地址或签名查询参数，离线详情严格保持零网络请求。"},
				{Type: "header-two", Text: "1.1 支持的富文本块类型"},
				{Type: "ordered-list-item", Text: "兼容 X 实盘的数组形态与对象形态 entityMap 解析"},
				{Type: "unordered-list-item", Text: "内嵌图片支持点击进入本地看图层缩放浏览"},
				{Type: "atomic", MediaRel: "images/01.png"},
				{Type: "atomic", TweetID: "1000000000000000018"},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	// Seed recent parse history with local avatar & cover thumbnails in <DataDir>/thumbs/ across all 4 sites.
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

	// Seed local account avatars under <DataDir>/avatars/<site>-<sha256[:10]>.png
	// so subscriptions, rule batch selection, queue rows, and incident sessions render local avatars offline.
	avatarsDir := filepath.Join(loc.DataDir, "avatars")
	_ = os.MkdirAll(avatarsDir, 0o700)
	avatarTargets := []struct {
		site string
		uid  string
		hue  uint8
	}{
		{"bilibili", "20001", 30},
		{"bilibili", "20002", 80},
		{"bilibili", "20003", 130},
		{"bilibili", "20004", 170},
		{"bilibili", "20005", 200},
		{"bilibili", "20006", 230},
		{"douyin", env.douyinSecUID, 55},
		{"douyin", "MS4wLjABAAAA_uishots_creator_02", 145},
		{"weibo", env.weiboUID, 15},
		{"weibo", "500000002", 110},
		{"x", env.xUID, 45},
		{"x", env.xOnlyUID, 125},
		{"x", "1000000000000000004", 190},
		{"x", "1000000000000000005", 220},
	}
	for _, at := range avatarTargets {
		avImg := image.NewRGBA(image.Rect(0, 0, 80, 80))
		for y := 0; y < 80; y++ {
			for x := 0; x < 80; x++ {
				avImg.SetRGBA(x, y, color.RGBA{
					R: uint8((int(at.hue) + (x*120)/80) % 230),
					G: uint8(70 + (y*120)/80),
					B: uint8(210 - int(at.hue)/3),
					A: 255,
				})
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, avImg)
		sum := sha256.Sum256([]byte(at.site + ":" + at.uid))
		avName := at.site + "-" + hex.EncodeToString(sum[:10]) + ".png"
		_ = os.WriteFile(filepath.Join(avatarsDir, avName), buf.Bytes(), 0o600)
	}

	histAv1, _ := b.HistoryStore().SaveThumb("video", "BV1xx411c7mD", "avatar", ".png", avatarBuf.Bytes())
	histCv1, _ := b.HistoryStore().SaveThumb("video", "BV1xx411c7mD", "cover", ".png", coverBuf.Bytes())
	histAv2, _ := b.HistoryStore().SaveThumb("user", "20002", "avatar", ".png", avatarBuf.Bytes())
	histAvDy, _ := b.HistoryStore().SaveThumb("video", "7388001001", "avatar", ".png", avatarBuf.Bytes())
	histCvDy, _ := b.HistoryStore().SaveThumb("video", "7388001001", "cover", ".png", portraitBuf.Bytes())
	histAvWb, _ := b.HistoryStore().SaveThumb("dynamic", "5000000000000001", "avatar", ".png", avatarBuf.Bytes())
	histCvWb, _ := b.HistoryStore().SaveThumb("dynamic", "5000000000000001", "cover", ".png", coverBuf.Bytes())
	histAvX, _ := b.HistoryStore().SaveThumb("dynamic", "1000000000000000009", "avatar", ".png", avatarBuf.Bytes())
	histCvX, _ := b.HistoryStore().SaveThumb("dynamic", "1000000000000000009", "cover", ".png", coverBuf.Bytes())
	histAvXUser, _ := b.HistoryStore().SaveThumb("user", env.xUID, "avatar", ".png", avatarBuf.Bytes())
	_ = b.HistoryStore().Record(storage.HistoryEntry{
		Site:         "weibo",
		Kind:         "dynamic",
		ID:           "5000000000000001",
		Title:        "微博长文与多图实况示例",
		Subtitle:     "技术频道_微博",
		CanonicalURL: "https://weibo.com/500000001/5000000000000001",
		AvatarThumb:  histAvWb,
		CoverThumb:   histCvWb,
	})
	_ = b.HistoryStore().Record(storage.HistoryEntry{
		Site:         "x",
		Kind:         "user",
		ID:           env.xUID,
		Title:        "技术频道_X",
		Subtitle:     "@" + env.xHandle,
		CanonicalURL: "https://x.com/" + env.xHandle,
		AvatarThumb:  histAvXUser,
	})
	_ = b.HistoryStore().Record(storage.HistoryEntry{
		Site:         "x",
		Kind:         "x_list",
		ID:           "18000000001",
		Title:        "Go 开源架构观察",
		Subtitle:     "12 位成员",
		CanonicalURL: "https://x.com/i/lists/18000000001",
	})
	_ = b.HistoryStore().Record(storage.HistoryEntry{
		Site:         "x",
		Kind:         "dynamic",
		ID:           "1000000000000000009",
		Title:        "X 长文与原图混排归档示例",
		Subtitle:     "X 合成专栏",
		CanonicalURL: "https://x.com/" + env.xOnlyHandle + "/status/1000000000000000009",
		AvatarThumb:  histAvX,
		CoverThumb:   histCvX,
	})
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

	env.backend = &screenshotBackend{
		BackendAPI: b,
		mineMediaPaths: map[string]string{
			"weibo:dynamic:5000000000000011::images/01.png": filepath.Join(wbPostDir, "img_01.png"),
			"weibo:dynamic:5000000000000011::images/02.png": filepath.Join(wbRetweetDir, "images", "02.png"),
			"weibo:dynamic:5000000000000012::images/01.png": filepath.Join(wbRetweetDir, "images", "01.png"),
			"weibo:dynamic:5000000000000012::images/02.png": filepath.Join(wbRetweetDir, "images", "02.png"),
			"weibo:dynamic:5000000000000013::cover.png":     filepath.Join(wbRetweetDir, "cover.png"),
			"x:dynamic:1000000000000000021::images/01.png":  filepath.Join(xPostDir, "images", "01.png"),
			"x:dynamic:1000000000000000022::images/01.png":  filepath.Join(xRetweetDir, "images", "01.png"),
		},
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
		wbStatus: credential.Status{
			Site:     "weibo",
			LoggedIn: true,
			State:    "logged_in",
			Source:   "manual",
			UID:      "100000001",
			Uname:    "TRACE微博演示账号",
		},
		xStatus: credential.Status{
			Site:     "x",
			LoggedIn: true,
			State:    "logged_in",
			Source:   "manual",
			UID:      "1000000000000000000",
			UniqueID: "trace_x_demo",
			Uname:    "TRACE X 演示账号",
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
			"weibo": {
				Site:        "weibo",
				DisplayName: "微博",
				LastCheckAt: "2026-11-12T10:10:00Z",
				NextCheckAt: "2026-11-12T11:10:00Z",
			},
			"x": {
				Site:        "x",
				DisplayName: "X",
				LastCheckAt: "2026-11-12T10:15:00Z",
				NextCheckAt: "2026-11-12T11:15:00Z",
			},
		},
	}

	// One newer and one older cached post for two more subscriptions, so
	// that the Home panel shows unread and read rows (DEC-053 ②).
	for _, p := range []struct {
		mid   int64
		name  string
		pubTs int64
	}{{20003, "数字影像志", 1710004000}, {20004, "开源架构师", 1709990000}} {
		id := fmt.Sprintf("uishots_dyn_%d", p.mid)
		if _, err := b.LibraryStore().SaveItem(filepath.Join(b.LibraryStore().CacheDir(), "dynamic", id), library.ItemMetadata{
			Version: 1, ID: id, RawID: id, Site: "bilibili", Kind: "dynamic", Status: "complete",
			Title: p.name + " 的图文动态", Text: p.name + " 的图文动态", PubTs: p.pubTs, CachedAt: "2026-03-02T10:00:00Z",
			Author: library.ItemAuthor{Mid: p.mid, Name: p.name},
		}); err != nil {
			return nil, err
		}
	}
	return env, nil
}

func (e *fixtureEnv) seedRecentAndLive(app *ui.App) {
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
			Incidents: []library.LiveIncident{
				{Kind: library.LiveIncidentLostSegments, At: time.Now().Add(-14 * time.Minute).UTC().Format(time.RFC3339), LostSegments: 3, DurationSec: 6},
				{Kind: library.LiveIncidentSlow, At: time.Now().Add(-8 * time.Minute).UTC().Format(time.RFC3339), DurationSec: 36},
			},
		},
	}
	app.LiveAccounts = []service.LiveAccountState{
		{
			Site:              "douyin",
			AccountKey:        "douyin:" + e.douyinSecUID,
			RecordingKey:      "douyin:live:73990011",
			UID:               e.douyinSecUID,
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
			Incidents: []library.LiveIncident{
				{Kind: library.LiveIncidentReconnect, At: time.Now().Add(-6 * time.Minute).UTC().Format(time.RFC3339), DurationSec: 4.2},
				{Kind: library.LiveIncidentLostSegments, At: time.Now().Add(-6 * time.Minute).UTC().Format(time.RFC3339), DurationSec: 18},
			},
		},
	}
	e.backend.rooms = append([]live.RoomState(nil), app.LiveRooms...)
	e.backend.liveAccts = append([]service.LiveAccountState(nil), app.LiveAccounts...)
	e.backend.queue = []service.CacheQueueTask{
		{
			ID:           "cq_demo_bili",
			Site:         "bilibili",
			User:         "20001",
			UserName:     "技术频道_Alpha",
			List:         "videos",
			ListLabel:    "投稿",
			Status:       service.CacheQueueRunning,
			StatusText:   "进行中",
			CachedCount:  18,
			SkippedCount: 4,
			ActiveTitle:  "纯 Go + MyGo 原生桌面架构全链路解析",
			RecentItems: []service.CacheQueueRecentItem{
				{ItemKey: e.savedItem.ItemKey, Title: e.savedItem.Title, CachedAt: "2026-11-15T06:00:00Z"},
			},
		},
		{
			ID:           "cq_demo_bili_2",
			Site:         "bilibili",
			User:         "20002",
			UserName:     "合成测评频道_Beta",
			List:         "dynamics",
			ListLabel:    "动态",
			Status:       service.CacheQueueQueued,
			StatusText:   "排队中",
			CachedCount:  0,
			SkippedCount: 0,
		},
		{
			ID:           "cq_demo_dy",
			Site:         "douyin",
			User:         e.douyinSecUID,
			UserName:     "技术频道_抖音号",
			List:         "works",
			ListLabel:    "作品",
			Status:       service.CacheQueueWaitingBackoff,
			StatusText:   "遇到风控，等待站点退避结束",
			StopReason:   "遇到风控，等待站点退避结束",
			CachedCount:  9,
			SkippedCount: 2,
			NextRunAt:    "2026-11-15T07:00:00Z",
		},
		{
			ID:           "cq_demo_wb",
			Site:         "weibo",
			User:         e.weiboUID,
			UserName:     "技术频道_微博",
			List:         "posts",
			ListLabel:    "微博",
			Status:       service.CacheQueueCompleted,
			StatusText:   "已完成",
			Note:         "博主设置了仅展示半年内的微博，已缓存全部可见原创微博",
			CachedCount:  12,
			SkippedCount: 3,
		},
		{
			ID:           "cq_demo_x",
			Site:         "x",
			User:         e.xUID,
			UserName:     "技术频道_X",
			List:         "posts",
			ListLabel:    "帖子",
			Status:       service.CacheQueueCompleted,
			StatusText:   "已完成",
			Note:         "已到达 X 时间线可见上限，已缓存全部可见原创帖子",
			CachedCount:  24,
			SkippedCount: 6,
		},
	}
	app.CacheQueue = e.backend.ListCacheQueue()
	e.backend.conflicts = []service.CacheConflictDTO{
		{
			ID:            "conflict_shot_bili",
			ItemKey:       e.savedItem.ItemKey,
			Site:          "bilibili",
			Kind:          "video",
			TargetID:      e.savedItem.RawID,
			AuthorUID:     "20001",
			AuthorName:    "技术频道_Alpha",
			Title:         e.savedItem.Title,
			DetectedAt:    time.Now().Add(-25 * time.Minute).UTC().Format(time.RFC3339),
			ReasonKind:    service.ConflictReasonParts,
			ReasonLabel:   service.ConflictReasonLabel(service.ConflictReasonParts),
			LocalSummary:  "共 1 P（P1 cid=101 · 4K HDR · Hi-Res 无损）",
			RemoteSummary: "共 2 P（P1 cid=209 重制版，新增 P2 勘误补充）",
			State:         service.ConflictStatePending,
		},
		{
			ID:            "conflict_shot_wb",
			ItemKey:       e.savedWeiboPost.ItemKey,
			Site:          "weibo",
			Kind:          "dynamic",
			TargetID:      e.savedWeiboPost.RawID,
			AuthorUID:     e.weiboUID,
			AuthorName:    "技术频道_微博",
			Title:         e.savedWeiboPost.Title,
			DetectedAt:    time.Now().Add(-50 * time.Minute).UTC().Format(time.RFC3339),
			ReasonKind:    service.ConflictReasonMedia,
			ReasonLabel:   service.ConflictReasonLabel(service.ConflictReasonMedia),
			LocalSummary:  "1 张原图（480×200，已校验完整）",
			RemoteSummary: "博主重新编辑微博，替换为 3 张修订配图",
			State:         service.ConflictStatePending,
		},
		{
			ID:            "conflict_shot_x",
			ItemKey:       e.savedXPost.ItemKey,
			Site:          "x",
			Kind:          "dynamic",
			TargetID:      e.savedXPost.RawID,
			AuthorUID:     e.xOnlyUID,
			AuthorName:    "X 合成专栏",
			Title:         e.savedXPost.Title,
			DetectedAt:    time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
			ReasonKind:    service.ConflictReasonText,
			ReasonLabel:   service.ConflictReasonLabel(service.ConflictReasonText),
			LocalSummary:  "长帖初版正文（含 1 张原图，已完整归档）",
			RemoteSummary: "作者编辑了帖子正文（edit_count=2）",
			State:         service.ConflictStateFailed,
			LastError:     "源站帖子当前不可访问或媒体已失效，已拒绝覆盖（本地原文件未改动）",
		},
	}
	app.CacheConflicts, _ = e.backend.ListCacheConflicts()
	app.CacheConflictPendingCount = e.backend.PendingCacheConflictCount()
}

func (e *fixtureEnv) injectMixedHomeFeed(app *ui.App) {
	app.PopulateSyntheticFeed(5000)
	if len(app.FeedItems) > 4 {
		// Showcase B 站, 微博, X, and 抖音 author badges simultaneously on Home across all 3 widths.
		app.FeedItems[1] = library.FeedEntry{
			FeedKey:      e.savedWeiboPost.ItemKey,
			Site:         "weibo",
			Kind:         "dynamic",
			RawID:        e.savedWeiboPost.RawID,
			AuthorUID:    e.weiboUID,
			AuthorName:   "技术频道_微博",
			Title:        e.savedWeiboPost.Title,
			Summary:      e.savedWeiboPost.Title,
			CanonicalURL: e.savedWeiboPost.URL,
			PubTs:        app.FeedItems[0].PubTs - 60,
			Cached:       true,
			CacheStatus:  "green",
		}
		app.FeedItems[2] = library.FeedEntry{
			FeedKey:      e.savedXPost.ItemKey,
			Site:         "x",
			Kind:         "dynamic",
			RawID:        e.savedXPost.RawID,
			AuthorUID:    e.xOnlyUID,
			AuthorName:   "X 合成专栏",
			Title:        e.savedXPost.Title,
			Summary:      e.savedXPost.Title,
			CanonicalURL: e.savedXPost.URL,
			PubTs:        app.FeedItems[0].PubTs - 90,
			Cached:       true,
			CacheStatus:  "green",
		}
		app.FeedItems[3] = library.FeedEntry{
			FeedKey:      e.savedDouyinNote.ItemKey,
			Site:         "douyin",
			Kind:         "dynamic",
			RawID:        e.savedDouyinNote.RawID,
			AuthorUID:    e.douyinSecUID,
			AuthorName:   "技术频道_抖音号",
			Title:        e.savedDouyinNote.Title,
			Summary:      e.savedDouyinNote.Text,
			CanonicalURL: e.savedDouyinNote.URL,
			PubTs:        app.FeedItems[0].PubTs - 120,
			Cached:       true,
			CacheStatus:  "green",
			ItemKey:      e.savedDouyinNote.ItemKey,
		}
	}
}
