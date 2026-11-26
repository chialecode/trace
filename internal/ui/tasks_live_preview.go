package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

type activeLiveRow struct {
	RecKey       string
	Site         string
	BadgeLabel   string
	StopBtnLabel string
	Uname        string
	UID          string
	Mid          int64
	Face         string
	RoomID       int64
	RoomIDStr    string
	Title        string
	QualSpec     string
	DurationSec  int
	Bytes        int64
	SpeedBps     int64
	Segments     int
	ItemDir      string
	Incidents    []library.LiveIncident
}

func (a *App) collectActiveLiveRows() []activeLiveRow {
	var rows []activeLiveRow
	for _, room := range a.LiveRooms {
		if !room.Recording {
			continue
		}
		qualSpec := bilibili.FormatLiveQualitySpec(room.QualityName, room.Quality, room.Codec)
		roomDec := strconv.FormatInt(room.RoomID, 10)
		uidStr := strconv.FormatInt(room.UID, 10)
		rows = append(rows, activeLiveRow{
			RecKey:       fmt.Sprintf("bilibili:live:%d", room.RoomID),
			Site:         "bilibili",
			BadgeLabel:   fmt.Sprintf("录制站点：bilibili:%d", room.RoomID),
			StopBtnLabel: fmt.Sprintf("任务停止录制 %d", room.RoomID),
			Uname:        firstNonEmptyStrUI(room.Uname, fmt.Sprintf("UID %d", room.UID)),
			UID:          uidStr,
			Mid:          room.UID,
			RoomID:       room.RoomID,
			RoomIDStr:    roomDec,
			Title:        firstNonEmptyStrUI(room.Title, "哔哩哔哩直播"),
			QualSpec:     qualSpec,
			DurationSec:  int(room.RecordDurationSec),
			Bytes:        room.RecordBytes,
			SpeedBps:     room.RecordSpeedBps,
			Segments:     max(1, room.RecordSegments),
			ItemDir:      room.RecordItemDir,
			Incidents:    a.resolveActiveRowIncidents("bilibili", roomDec, uidStr, room.RecordItemDir, room.Incidents),
		})
	}
	for _, acct := range a.LiveAccounts {
		if normalizeSiteIDUI(acct.Site) != "douyin" || !acct.Recording {
			continue
		}
		recKey := firstNonEmptyStrUI(acct.RecordingKey, acct.AccountKey, acct.RoomID, acct.UID)
		badgeKey := firstNonEmptyStrUI(acct.AccountKey, "douyin:"+acct.UID)
		rows = append(rows, activeLiveRow{
			RecKey:       recKey,
			Site:         "douyin",
			BadgeLabel:   "录制站点：" + badgeKey,
			StopBtnLabel: "任务停止录制 " + badgeKey,
			Uname:        firstNonEmptyStrUI(acct.Uname, acct.UID),
			UID:          acct.UID,
			Face:         acct.Face,
			RoomIDStr:    acct.RoomID,
			Title:        firstNonEmptyStrUI(acct.Title, "抖音直播"),
			QualSpec:     formatLiveAccountQualitySpec(acct),
			DurationSec:  int(acct.RecordDurationSec),
			Bytes:        acct.RecordBytes,
			SpeedBps:     acct.RecordSpeedBps,
			Segments:     max(1, acct.RecordSegments),
			ItemDir:      acct.RecordItemDir,
			Incidents:    a.resolveActiveRowIncidents("douyin", acct.RoomID, acct.UID, acct.RecordItemDir, acct.Incidents),
		})
	}
	return rows
}

func (a *App) renderTasksLiveView(c *ui.Context) {
	rows := a.collectActiveLiveRows()
	if len(rows) == 0 && a.LivePreviewKey != "" {
		a.closeLivePreview()
	}

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(12).ClipX().Children(func() {
			if len(rows) == 0 {
				ui.Column(c).Label("直播录制空状态").FillWidth().Padding(48, 16).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "现在没有直播在录制").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, "在订阅页、主播空间或直播间点「录制」后，正在录制的直播会显示在这里，点录制行可实时预览画面。").
						FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
				})
			}

			for _, item := range rows {
				row := item
				previewing := a.LivePreviewKey != "" && a.matchesLivePreviewKey(row.RecKey, row.RoomID, row.UID)
				incExpanded := len(row.Incidents) > 0 && a.isLiveRowIncidentsExpanded(row.RecKey)
				borderCol := ColorLive
				if previewing {
					borderCol = ColorAccent
				}
				ui.Column(c).
					Label("正在录制行："+row.RecKey).
					FillWidth().
					Padding(12, 14).
					Radius(16).
					Background(ColorLiveBg).
					Border(1, borderCol).
					Gap(10).
					ClipX().
					Children(func() {
						ui.Row(c).
							FillWidth().
							AlignItems(ui.Center).
							Justify(ui.SpaceBetween).
							Gap(12).
							Wrap().
							ClipX().
							Children(func() {
								clickArea := ui.ButtonBase(c).
									Label("预览直播录制："+row.RecKey).
									Grow(1).
									Shrink(1).
									Gap(12).
									AlignItems(ui.Center).
									Justify(ui.Start).
									Radius(10).
									Padding(2, 4).
									ClipX()
								if clickArea.Hovered() {
									clickArea.Background(ColorHover)
								}
								clickArea.Children(func() {
									a.renderAccountAvatar(c, row.Site, row.Uname, row.UID, row.Mid, 42, true, "", nil, row.Face)
									ui.Column(c).Grow(1).Shrink(1).Gap(4).ClipX().Children(func() {
										ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
											ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorLive).Children(func() {
												ui.Text(c, "录制中").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
											})
											if previewing {
												ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorAccentBg).Children(func() {
													ui.Text(c, "正在预览").FontSize(11).FontWeight(600).TextColor(ColorAccent).SingleLine()
												})
											}
											ui.Text(c, row.Uname+" · "+row.Title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
											renderSiteSmallBadgeWithDot(c, row.Site, row.BadgeLabel)
											if row.QualSpec != "" {
												ui.Text(c, "· "+row.QualSpec).Shrink(0).FontSize(12).FontWeight(600).TextColor(ColorLive).SingleLine()
											}
										})
										recMeta := fmt.Sprintf("已录时长 %s · 大小 %s · 速度 %s/s · 第 %d 段 · 点击实时预览",
											FormatDuration(row.DurationSec),
											FormatBytes(row.Bytes),
											FormatBytes(row.SpeedBps),
											row.Segments)
										ui.Text(c, recMeta).FontSize(12).TextColor(ColorTextMuted).SingleLine()
									})
								})
								if clickArea.Clicked() {
									a.StartLivePreview(row.RecKey)
								}

								ui.Row(c).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
									if len(row.Incidents) > 0 {
										renderLiveRowIncidentPill(c, row.RecKey, len(row.Incidents), incExpanded, func() {
											a.toggleLiveRowIncidents(row.RecKey)
										})
									}
									if DangerOutlinePillButton(c, "停止录制", row.StopBtnLabel).Clicked() {
										if row.Site == "bilibili" && row.RoomID > 0 {
											a.stopLiveRecording(row.RoomID)
										} else {
											a.stopAccountLiveRecording(row.RecKey)
										}
									}
								})
							})

						if incExpanded {
							a.renderInlineRecordingIncidents(c, row.RecKey, row.Incidents, nil)
						}
					})
			}

			if a.LivePreviewKey != "" {
				a.renderLivePreviewCard(c)
			}

			a.renderRecentFinishedLiveSection(c, rows)
		})
	})
}

func (a *App) renderLivePreviewCard(c *ui.Context) {
	var snap player.StateSnapshot
	if a.livePreviewPlayer != nil {
		snap = a.livePreviewPlayer.Snapshot(640, 360)
	}

	ui.Column(c).
		Label("直播实时预览面板").
		FillWidth().
		Padding(14).
		Radius(16).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(10).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).ClipX().Children(func() {
					ui.Box(c).Label("直播实时预览标记").Shrink(0).Padding(2, 8).Radius(999).Background(ColorLive).Children(func() {
						ui.Text(c, "实时预览").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
					})
					ui.Text(c, firstNonEmptyStrUI(a.LivePreviewTitle, "正在录制的直播")).
						FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1).Shrink(1)
					if a.LivePreviewPartIndex > 0 {
						ui.Text(c, fmt.Sprintf("第 %d 段", a.LivePreviewPartIndex)).
							FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
					}
				})
				if OutlinePillButton(c, "关闭预览", "关闭直播实时预览").Clicked() {
					a.closeLivePreview()
				}
			})

			// Video surface: strictly no danmaku rendering and no danmaku controls (DEC-049 ①).
			ui.Column(c).
				Label("直播实时预览画面").
				FillWidth().
				Height(280).
				Radius(12).
				Background(ColorBlack).
				Clip().
				Center().
				Children(func() {
					switch {
					case a.LivePreviewUnsupported:
						ui.Column(c).Padding(24).AlignItems(ui.Center).Gap(6).Children(func() {
							ui.Text(c, "这场录制暂不支持实时预览").
								Label("直播不支持实时预览提示").
								FontSize(14).FontWeight(600).TextColor(ColorWhite).SingleLine()
							if a.LivePreviewReason != "" && a.LivePreviewReason != "这场录制暂不支持实时预览" {
								ui.Text(c, a.LivePreviewReason).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.72)).MaxLines(2)
							}
						})
					case a.LivePreviewError != "":
						ui.Column(c).Padding(24).AlignItems(ui.Center).Gap(10).Children(func() {
							ui.Text(c, a.LivePreviewError).FontSize(13).FontWeight(600).TextColor(ColorError).MaxLines(2)
							if OutlinePillButton(c, "重试", "重试直播实时预览").Clicked() {
								key := a.LivePreviewKey
								a.StartLivePreview(key)
							}
						})
					case a.LivePreviewLoading:
						ui.Column(c).AlignItems(ui.Center).Gap(8).Children(func() {
							ui.Spinner(c).Label("直播预览加载中").Size(24, 24)
							ui.Text(c, "正在读取本机录制分片…").FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.75)).SingleLine()
						})
					default:
						if a.livePreviewPlayer != nil {
							if bmp := a.livePreviewPlayer.Bitmap(); bmp != nil {
								ui.Image(c, bmp).Fill().Fit(ui.Contain)
							}
						}
					}
				})

			if !a.LivePreviewUnsupported && a.LivePreviewError == "" {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).Wrap().ClipX().Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						playText := "暂停"
						if !snap.Playing {
							playText = "继续播放"
						}
						if OutlinePillButton(c, playText, "直播预览播放暂停").Disabled(a.LivePreviewLoading).Clicked() && a.livePreviewPlayer != nil {
							a.livePreviewPlayer.TogglePlay()
						}
						ui.Text(c, fmt.Sprintf("已播 %s · 读取本机录制文件（不另开站点连接）", FormatDuration(int(snap.PositionSec)))).
							FontSize(12).TextColor(ColorTextMuted).SingleLine()
					})

					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						muted := snap.Muted
						if ui.Switch(c, &muted).Label("直播预览静音").Changed() && a.livePreviewPlayer != nil {
							st := a.fs()
							st.livePreviewMutedSet = true
							st.livePreviewMuted = muted
							a.livePreviewPlayer.SetMuted(muted)
						}
						ui.Text(c, "静音").FontSize(12).TextColor(ColorTextMuted).SingleLine()
						vol := a.PlayerOverlay.Volume
						if ui.Slider(c, &vol, 0, 1).Label("直播预览音量").Width(100).Changed() && a.livePreviewPlayer != nil {
							st := a.fs()
							st.livePreviewMutedSet = true
							st.livePreviewMuted = false
							a.PlayerOverlay.Volume = vol
							a.livePreviewPlayer.SetVolume(vol)
							a.livePreviewPlayer.SetMuted(false)
						}
					})
				})
			}
		})
}

func (a *App) matchesLivePreviewKey(recKey string, roomID int64, uid string) bool {
	if a.LivePreviewKey == "" {
		return false
	}
	if a.LivePreviewKey == recKey {
		return true
	}
	if roomID > 0 {
		dec := strconv.FormatInt(roomID, 10)
		if a.LivePreviewKey == dec || a.LivePreviewKey == "bilibili:live:"+dec {
			return true
		}
	}
	if uid != "" && (a.LivePreviewKey == uid || a.LivePreviewKey == "douyin:"+uid || a.LivePreviewKey == "bilibili:"+uid) {
		return true
	}
	return false
}

func (a *App) lookupLiveRecordingPreview(recKey string) (service.LiveRecordingPreviewInfo, bool) {
	target := strings.TrimSpace(recKey)
	if target == "" {
		return service.LiveRecordingPreviewInfo{}, false
	}
	if a.backend != nil {
		if info, ok := a.backend.LiveRecordingPreview(target); ok {
			return info, true
		}
	}
	// Fallback to in-memory LiveRooms / LiveAccounts when tests or screenshot tools seed state directly.
	for _, r := range a.LiveRooms {
		biliKey := fmt.Sprintf("bilibili:live:%d", r.RoomID)
		roomDec := strconv.FormatInt(r.RoomID, 10)
		acctKey := fmt.Sprintf("bilibili:%d", r.UID)
		if target != biliKey && target != roomDec && target != acctKey {
			continue
		}
		partPath := r.RecordPartPath
		if partPath == "" && r.RecordItemDir != "" {
			partPath = newestPartFileInDir(r.RecordItemDir)
		}
		supported := r.PreviewSupported
		if !supported && !r.AudioOnly && r.PreviewReason == "" && r.Recording && partPath != "" {
			supported = true
		}
		return service.LiveRecordingPreviewInfo{
			RecordingKey:     biliKey,
			Site:             "bilibili",
			AccountUID:       strconv.FormatInt(r.UID, 10),
			RoomID:           roomDec,
			Uname:            r.Uname,
			Title:            r.Title,
			Codec:            r.Codec,
			Quality:          r.Quality,
			QualityName:      r.QualityName,
			Transport:        r.Transport,
			AudioOnly:        r.AudioOnly,
			Recording:        r.Recording,
			PartPath:         partPath,
			PartIndex:        max(1, r.RecordPartIndex),
			PartBytes:        r.RecordPartBytes,
			PartReady:        r.RecordPartReady,
			PreviewSupported: supported,
			PreviewReason:    r.PreviewReason,
			ItemDir:          r.RecordItemDir,
			DurationSec:      r.RecordDurationSec,
			SpeedBps:         r.RecordSpeedBps,
			BytesWritten:     r.RecordBytes,
			Segments:         r.RecordSegments,
			Incidents:        library.SanitizeLiveIncidents(r.Incidents),
		}, true
	}
	for _, st := range a.LiveAccounts {
		if st.RecordingKey != target && st.AccountKey != target && st.RoomID != target && st.UID != target && st.WebRID != target {
			continue
		}
		partPath := st.RecordPartPath
		if partPath == "" && st.RecordItemDir != "" {
			partPath = newestPartFileInDir(st.RecordItemDir)
		}
		supported := st.PreviewSupported
		if !supported && !st.AudioOnly && st.PreviewReason == "" && st.Recording && partPath != "" {
			supported = true
		}
		return service.LiveRecordingPreviewInfo{
			RecordingKey:     firstNonEmptyStrUI(st.RecordingKey, st.AccountKey),
			Site:             st.Site,
			AccountUID:       st.UID,
			RoomID:           st.RoomID,
			WebRID:           st.WebRID,
			Uname:            st.Uname,
			Title:            st.Title,
			Codec:            st.Codec,
			QualityName:      st.QualityName,
			Transport:        st.Transport,
			AudioOnly:        st.AudioOnly,
			Recording:        st.Recording,
			PartPath:         partPath,
			PartIndex:        max(1, st.RecordPartIndex),
			PartBytes:        st.RecordPartBytes,
			PartReady:        st.RecordPartReady,
			PreviewSupported: supported,
			PreviewReason:    st.PreviewReason,
			ItemDir:          st.RecordItemDir,
			DurationSec:      st.RecordDurationSec,
			SpeedBps:         st.RecordSpeedBps,
			BytesWritten:     st.RecordBytes,
			Segments:         st.RecordSegments,
			Incidents:        library.SanitizeLiveIncidents(st.Incidents),
		}, true
	}
	return service.LiveRecordingPreviewInfo{}, false
}

func newestPartFileInDir(dir string) string {
	entries, err := os.ReadDir(strings.TrimSpace(dir))
	if err != nil {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(e.Name()), ".part") {
			continue
		}
		info, iErr := e.Info()
		if iErr != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if newest == "" || info.ModTime().After(newestMod) {
			newest = full
			newestMod = info.ModTime()
		}
	}
	return newest
}

// StartLivePreview opens the local growing .part file of an active live recording
// for real-time in-page preview without opening any network connection (DEC-049 ①).
func (a *App) StartLivePreview(recKey string) {
	a.startLivePreview(recKey, false)
}

func (a *App) startLivePreview(recKey string, preserveMute bool) {
	recKey = strings.TrimSpace(recKey)
	if recKey == "" {
		return
	}
	st := a.fs()
	savedMuteSet, savedMuted := st.livePreviewMutedSet, st.livePreviewMuted
	a.closeLivePreview()
	if preserveMute && savedMuteSet {
		st.livePreviewMutedSet = true
		st.livePreviewMuted = savedMuted
	}
	a.Navigate(RouteTasks)
	a.TasksSection = "live"

	info, ok := a.lookupLiveRecordingPreview(recKey)
	if !ok || !info.Recording {
		return
	}

	title := strings.TrimSpace(info.Uname)
	if info.Title != "" {
		if title != "" {
			title += " · " + info.Title
		} else {
			title = info.Title
		}
	}
	a.LivePreviewKey = recKey
	a.LivePreviewTitle = title
	a.LivePreviewPartPath = info.PartPath
	a.LivePreviewPartIndex = max(1, info.PartIndex)
	a.LivePreviewUnsupported = false
	a.LivePreviewReason = ""
	a.LivePreviewError = ""

	if info.AudioOnly || !info.PreviewSupported || strings.TrimSpace(info.PartPath) == "" {
		a.LivePreviewUnsupported = true
		a.LivePreviewReason = info.PreviewReason
		return
	}

	a.livePreviewOpenSeq++
	seq := a.livePreviewOpenSeq
	ctx, cancel := context.WithCancel(a.ctx)
	a.livePreviewCancel = cancel
	a.LivePreviewLoading = true

	partPath := info.PartPath
	backend := a.backend
	hadBackendEntry := false
	if backend != nil {
		_, hadBackendEntry = backend.LiveRecordingPreview(recKey)
	}
	opener := a.livePreviewOpener
	if opener == nil {
		opener = player.OpenLivePreview
	}

	opts := player.LivePreviewOptions{
		IsActive: func() bool {
			if ctx.Err() != nil {
				return false
			}
			if backend != nil {
				if cur, exists := backend.LiveRecordingPreview(recKey); exists {
					return cur.Recording && (cur.PartPath == "" || cur.PartPath == partPath)
				}
				if hadBackendEntry {
					return false
				}
			}
			return true
		},
	}

	isCurrent := func() bool {
		return a.livePreviewOpenSeq == seq &&
			a.LivePreviewKey == recKey &&
			a.CurrentRoute == RouteTasks &&
			a.effectiveTasksSection() == "live"
	}

	requestGuardedCleanup(a, fmt.Sprintf("live-preview-open-%d", seq), isCurrent, func(reqCtx context.Context) (player.MediaDecoder, error) {
		dec, err := opener(ctx, partPath, opts)
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil || reqCtx.Err() != nil {
			if dec != nil {
				_ = dec.Close()
			}
			return nil, context.Canceled
		}
		return dec, nil
	}, func(dec player.MediaDecoder) {
		a.LivePreviewLoading = false
		if a.livePreviewPlayer == nil {
			if dec != nil {
				_ = dec.Close()
			}
			return
		}
		if err := a.livePreviewPlayer.LoadLiveDecoder(partPath, dec); err != nil {
			if errors.Is(err, player.ErrLivePreviewUnsupported) {
				a.LivePreviewUnsupported = true
				a.LivePreviewReason = err.Error()
			} else {
				a.LivePreviewError = "无法播放实时预览：" + err.Error()
			}
			return
		}
		a.livePreviewPlayer.SetVolume(a.PlayerOverlay.Volume)
		if st := a.fs(); st.livePreviewMutedSet {
			a.livePreviewPlayer.SetMuted(st.livePreviewMuted)
		} else if a.backend != nil {
			a.livePreviewPlayer.SetMuted(a.backend.GetUIState().Player.Muted)
		}
		a.livePreviewPlayer.Play()
	}, func(err error) {
		a.LivePreviewLoading = false
		if errors.Is(err, player.ErrLivePreviewUnsupported) {
			a.LivePreviewUnsupported = true
			a.LivePreviewReason = err.Error()
			return
		}
		a.LivePreviewError = "实时预览暂时无法打开：" + userErrorText(err)
	}, func(dec player.MediaDecoder) {
		if dec != nil {
			_ = dec.Close()
		}
	})
}

// OpenLivePreviewWithDecoder loads a pre-opened live decoder synchronously (used by headless screenshot assertions).
func (a *App) OpenLivePreviewWithDecoder(recKey, title, partPath string, partIndex int, dec player.MediaDecoder) error {
	a.closeLivePreview()
	a.CurrentRoute = RouteTasks
	a.TasksSection = "live"
	a.LivePreviewKey = recKey
	a.LivePreviewTitle = title
	a.LivePreviewPartPath = partPath
	a.LivePreviewPartIndex = max(1, partIndex)
	if a.livePreviewPlayer == nil {
		if dec != nil {
			_ = dec.Close()
		}
		return errors.New("live preview player not initialized")
	}
	if err := a.livePreviewPlayer.LoadLiveDecoder(partPath, dec); err != nil {
		return err
	}
	a.livePreviewPlayer.Play()
	a.livePreviewPlayer.Advance(33 * time.Millisecond)
	return nil
}

func (a *App) closeLivePreview() {
	a.livePreviewOpenSeq++
	if a.livePreviewCancel != nil {
		a.livePreviewCancel()
		a.livePreviewCancel = nil
	}
	if a.livePreviewPlayer != nil {
		_ = a.livePreviewPlayer.Close()
	}
	st := a.fs()
	st.livePreviewMutedSet = false
	st.livePreviewMuted = false
	a.LivePreviewKey = ""
	a.LivePreviewTitle = ""
	a.LivePreviewPartPath = ""
	a.LivePreviewPartIndex = 0
	a.LivePreviewLoading = false
	a.LivePreviewUnsupported = false
	a.LivePreviewReason = ""
	a.LivePreviewError = ""
}

func (a *App) closeLivePreviewForRoom(roomID int64) {
	if a.matchesLivePreviewKey(fmt.Sprintf("bilibili:live:%d", roomID), roomID, "") {
		a.closeLivePreview()
	}
}

func (a *App) closeLivePreviewForKey(recKey string) {
	if a.LivePreviewKey == "" {
		return
	}
	if a.LivePreviewKey == recKey {
		a.closeLivePreview()
		return
	}
	for _, acct := range a.LiveAccounts {
		if acct.RecordingKey == recKey || acct.AccountKey == recKey || acct.RoomID == recKey || acct.UID == recKey {
			roomID, _ := strconv.ParseInt(strings.TrimSpace(acct.RoomID), 10, 64)
			if a.matchesLivePreviewKey(firstNonEmptyStrUI(acct.RecordingKey, acct.AccountKey), roomID, acct.UID) {
				a.closeLivePreview()
				return
			}
		}
	}
}

// syncLivePreviewState closes the preview if its recording stopped, or follows a newly rotated .part segment.
func (a *App) syncLivePreviewState() {
	if a.LivePreviewKey == "" {
		return
	}
	if a.CurrentRoute != RouteTasks || a.effectiveTasksSection() != "live" {
		a.closeLivePreview()
		return
	}
	info, ok := a.lookupLiveRecordingPreview(a.LivePreviewKey)
	if !ok || !info.Recording {
		a.closeLivePreview()
		return
	}
	if info.PartIndex > 0 {
		a.LivePreviewPartIndex = info.PartIndex
	}
	if !a.LivePreviewUnsupported && info.PartPath != "" && a.LivePreviewPartPath != "" && info.PartPath != a.LivePreviewPartPath {
		key := a.LivePreviewKey
		a.startLivePreview(key, true)
	}
}
