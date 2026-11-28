// Package pastecache implements the reusable "粘贴链接缓存" and Android Share
// Intent (ACTION_SEND) submission flow for TRACE mobile (FR-MOB-030).
// Mobile never visits source websites or previews online details: it sends the
// raw URL / share text to the paired desktop's POST /api/v1/parse-and-cache.
package pastecache

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/mediaview"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

// Result aliases the canonical desktop parse-and-cache response DTO (FR-MOB-030).
type Result = companionapi.ParseAndCacheResponse

// Submit sends raw URL or share text to the paired desktop to parse and enqueue
// caching directly via Client.ParseAndCache. Mobile makes zero direct site requests.
func Submit(ctx context.Context, cli *companionclient.Client, input string) (Result, error) {
	if cli == nil {
		return Result{}, &companionclient.Error{Kind: companionclient.KindUnreachable}
	}
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return Result{}, errors.New("请先粘贴或输入要缓存的链接")
	}
	return cli.ParseAndCache(ctx, trimmed)
}

// Sheet manages the "粘贴链接让电脑缓存" bottom sheet & Share Intent progress UI.
type Sheet struct {
	mu           sync.Mutex
	open         bool
	uiOpen       bool // UI-thread mirror passed directly to mobile.Sheet
	fromShare    bool
	input        string
	composing    bool
	submitting   bool
	gen          uint64
	result       *Result
	errMsg       string
	onQueuedHook func()

	// Optional test override for Submit.
	SubmitFn func(ctx context.Context, cli *companionclient.Client, input string) (Result, error)
}

// SetOnQueued registers an optional callback triggered when a new cache task is queued.
func (s *Sheet) SetOnQueued(fn func()) {
	s.mu.Lock()
	s.onQueuedHook = fn
	s.mu.Unlock()
}

// IsOpen reports whether the paste/share sheet is currently open.
func (s *Sheet) IsOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open
}

// Open displays the paste-link sheet with optional prefilled input.
func (s *Sheet) Open(prefill string) {
	s.mu.Lock()
	s.open = true
	s.fromShare = false
	if strings.TrimSpace(prefill) != "" {
		s.input = strings.TrimSpace(prefill)
	}
	s.errMsg = ""
	s.result = nil
	s.mu.Unlock()
}

// SubmitShared opens the sheet for an incoming Android ACTION_SEND text payload
// and immediately sends it to the connected desktop to parse and cache.
func (s *Sheet) SubmitShared(h pagekit.Host, rawText string) {
	trimmed := strings.TrimSpace(rawText)
	if trimmed == "" {
		return
	}
	s.mu.Lock()
	// Supersede any in-flight share submission so a second consecutive share always runs.
	s.gen++
	s.submitting = false
	s.open = true
	s.fromShare = true
	s.input = trimmed
	s.errMsg = ""
	s.result = nil
	s.mu.Unlock()
	s.triggerSubmit(h)
}

// Reset clears all in-memory input, progress, and results (called on disconnect / unpair).
func (s *Sheet) Reset() {
	s.mu.Lock()
	s.gen++
	s.open = false
	s.fromShare = false
	s.input = ""
	s.composing = false
	s.submitting = false
	s.result = nil
	s.errMsg = ""
	s.mu.Unlock()
}

func (s *Sheet) triggerSubmit(h pagekit.Host) {
	s.mu.Lock()
	if s.submitting {
		s.mu.Unlock()
		return
	}
	raw := strings.TrimSpace(s.input)
	if raw == "" {
		s.errMsg = "请先粘贴或输入链接（支持 B 站、抖音、微博、X 链接或分享文案）"
		s.mu.Unlock()
		return
	}
	if h == nil || h.Client() == nil || h.ConnState() != pagekit.ConnConnected {
		s.errMsg = "当前未连接到电脑端，连接恢复后可重新发送"
		s.mu.Unlock()
		return
	}
	s.gen++
	gen := s.gen
	s.submitting = true
	s.errMsg = ""
	s.result = nil
	cli := h.Client()
	submitFn := s.SubmitFn
	onQueued := s.onQueuedHook
	s.mu.Unlock()

	if submitFn == nil {
		submitFn = Submit
	}
	h.Update(nil)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := submitFn(ctx, cli, raw)
		h.Update(func() {
			s.mu.Lock()
			if s.gen != gen {
				s.mu.Unlock()
				return
			}
			s.submitting = false
			if err != nil {
				s.errMsg = FormatError(err)
				s.result = nil
				s.mu.Unlock()
				return
			}
			resCopy := res
			s.result = &resCopy
			s.errMsg = ""
			s.mu.Unlock()
			if !res.AlreadyCached && onQueued != nil {
				onQueued()
			}
		})
	}()
}

// View renders the modal bottom sheet. Must be called unconditionally so
// mobile.Sheet tracks open/close spring animations cleanly across frames.
func (s *Sheet) View(c *ui.Context, h pagekit.Host) {
	s.mu.Lock()
	s.uiOpen = s.open
	fromShare := s.fromShare
	inputVal := s.input
	wasComposing := s.composing
	submitting := s.submitting
	var res *Result
	if s.result != nil {
		cp := *s.result
		res = &cp
	}
	errMsg := s.errMsg
	s.mu.Unlock()

	bg := theme.ColorBgMain
	title := "粘贴链接让电脑缓存"
	if fromShare {
		title = "分享到电脑缓存"
	}

	mobile.Sheet(c, &s.uiOpen, mobile.SheetOptions{
		Detents:    []float32{0.68, 0.90},
		Background: &bg,
		Label:      title,
	}, func() {
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).FillWidth().Padding(10, 16, 28, 16).Gap(12).ClipX().Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
					ui.Text(c, title).FontSize(17).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Grow(1).Shrink(1)
					closeBtn := ui.ButtonBase(c).Label("关闭粘贴链接面板").Padding(6, 10).MinHeight(36).Radius(999).Background(theme.ColorBgSecondary).Shrink(0)
					closeBtn.Children(func() {
						ui.Text(c, "关闭").FontSize(12).FontWeight(500).TextColor(theme.ColorTextMuted).SingleLine()
					})
					if closeBtn.Clicked() {
						s.uiOpen = false
					}
				})

				ui.Text(c, "手机不访问原站，也不预览线上详情；发送后由电脑端解析并直接加入缓存队列。").
					FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted)

				// Show result or error immediately above the input so 360dp screens never hide actions below the fold.
				if res != nil {
					s.renderResultCard(c, h, *res, &s.uiOpen)
				}
				if errMsg != "" {
					ui.Column(c).
						Label("粘贴链接错误提示").
						FillWidth().
						Padding(10, 12).
						Radius(12).
						Background(theme.ColorErrorBg).
						Border(1, ui.RGBA(196, 24, 46, 0.25)).
						Gap(8).
						ClipX().
						Children(func() {
							ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
								theme.DrawIcon(c, "alert-circle", 18, theme.ColorError, 1.8)
								ui.Text(c, errMsg).
									FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorError).MaxLines(4).Grow(1).Shrink(1)
							})
							if !submitting && strings.TrimSpace(inputVal) != "" {
								if theme.SecondaryPillButton(c, "重试发送", "重试发送链接缓存").Clicked() {
									s.triggerSubmit(h)
								}
							}
						})
				}

				// URL / share text input with IME composition safety (LESSONS.md).
				edited := inputVal
				inp := ui.TextInput(c, &edited).
					Label("粘贴链接或分享文案输入框").
					Placeholder("粘贴 B 站 / 抖音 / 微博 / X 链接或分享文案").
					Keyboard(ui.KeyboardURL).
					Capitalize(ui.CapitalizeNone).
					Autocorrect(false).
					ReturnKey(ui.ReturnSend).
					Disabled(submitting).
					MinHeight(46)
				curComposing := inp.Composing()
				submitted := inp.Submitted() && !curComposing && !wasComposing && !submitting

				s.mu.Lock()
				s.input = edited
				s.composing = curComposing
				s.mu.Unlock()

				// Clipboard & clear helper row.
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
					pasteBtn := theme.SecondaryPillButton(c, "从剪贴板粘贴", "从剪贴板粘贴链接").Disabled(submitting)
					if pasteBtn.Clicked() && !submitting {
						clip := strings.TrimSpace(c.ReadClipboard())
						s.mu.Lock()
						if clip != "" {
							s.input = clip
							s.errMsg = ""
							s.result = nil
						} else {
							s.errMsg = "剪贴板里没有可粘贴的文本"
						}
						s.mu.Unlock()
					}
					if strings.TrimSpace(edited) != "" && !submitting {
						if theme.SecondaryPillButton(c, "清空", "清空链接输入").Clicked() {
							s.mu.Lock()
							s.input = ""
							s.errMsg = ""
							s.result = nil
							s.mu.Unlock()
						}
					}
				})

				// Primary submit CTA.
				btnLbl := "发送到电脑缓存"
				if submitting {
					btnLbl = "正在由电脑端解析并加入缓存…"
				}
				sendDisabled := submitting || strings.TrimSpace(edited) == ""
				if theme.PrimaryPillButton(c, btnLbl, "发送到电脑缓存").FillWidth().Disabled(sendDisabled).Clicked() && !sendDisabled {
					s.triggerSubmit(h)
				}
				if submitted {
					s.triggerSubmit(h)
				}
			})
		})
	})

	if !s.uiOpen {
		s.mu.Lock()
		s.open = false
		s.mu.Unlock()
	}
}

func (s *Sheet) renderResultCard(c *ui.Context, h pagekit.Host, res Result, openFlag *bool) {
	bg := theme.ColorAccentBg
	borderCol := ui.RGBA(26, 115, 232, 0.30)
	heading := "已加入电脑缓存队列"
	if res.AlreadyCached {
		bg = theme.ColorBgPanel
		borderCol = theme.ColorBorderStrong
		heading = "电脑媒体库中已缓存该内容"
	}

	ui.Column(c).
		Label("粘贴链接结果卡片").
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(bg).
		Border(1, borderCol).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Wrap().Children(func() {
				ui.Text(c, heading).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
				if siteLbl := mediaview.SiteLabel(res.Site); siteLbl != "" {
					ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorBgMain).Border(1, theme.ColorBorder).Children(func() {
						ui.Text(c, siteLbl).FontSize(11).FontWeight(500).TextColor(theme.ColorTextMuted).SingleLine()
					})
				}
			})

			if title := strings.TrimSpace(res.Title); title != "" {
				ui.Text(c, title).FontSize(13).FontWeight(500).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMain).MaxLines(2)
			}
			var meta []string
			if author := strings.TrimSpace(res.AuthorName); author != "" {
				meta = append(meta, author)
			}
			if len(res.Tasks) > 1 {
				meta = append(meta, "已创建多个缓存任务")
			} else if res.TaskID != "" && !res.AlreadyCached {
				meta = append(meta, "任务已开始排队")
			}
			if msg := strings.TrimSpace(res.Message); msg != "" && msg != heading {
				meta = append(meta, msg)
			}
			if len(meta) > 0 {
				ui.Text(c, strings.Join(meta, " · ")).FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).MaxLines(3)
			}

			ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
				if res.AlreadyCached {
					if theme.PrimaryPillButton(c, "查看媒体库", "前往媒体库查看已缓存内容").Clicked() {
						*openFlag = false
						h.SelectTab(pagekit.TabLibrary)
					}
				} else {
					if theme.PrimaryPillButton(c, "查看任务", "前往任务页查看缓存进度").Clicked() {
						*openFlag = false
						h.SelectTab(pagekit.TabTasks)
					}
				}
				if theme.SecondaryPillButton(c, "继续粘贴", "继续粘贴新链接").Clicked() {
					s.mu.Lock()
					s.fromShare = false
					s.input = ""
					s.result = nil
					s.errMsg = ""
					s.mu.Unlock()
				}
			})
		})
}

// FormatError formats desktop parse-and-cache errors into clear Chinese user
// messages matching FR-ERR-001 (zero cookie/login prompts on phone).
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	var ce *companionclient.Error
	if errors.As(err, &ce) && ce != nil {
		code := strings.ToLower(strings.TrimSpace(ce.Code))
		switch code {
		case companionapi.CodeAuthRequired:
			if msg := strings.TrimSpace(ce.Message); msg != "" && strings.Contains(msg, "电脑") {
				return msg
			}
			switch strings.ToLower(strings.TrimSpace(ce.Site)) {
			case "bilibili":
				return "请先在电脑上登录哔哩哔哩，再重新发送链接"
			case "douyin":
				return "请先在电脑上登录抖音，再重新发送链接"
			case "weibo":
				return "请先在电脑上登录微博，再重新发送链接"
			case "x":
				return "请先在电脑上登录 X，再重新发送链接"
			}
			return "请先在电脑上登录对应站点，再重新发送链接"
		case companionapi.CodeRiskControl:
			if msg := strings.TrimSpace(ce.Message); msg != "" {
				return msg
			}
			return "触发站点风控，请稍后在电脑端检查并重试"
		case companionapi.CodeRateLimited:
			if msg := strings.TrimSpace(ce.Message); msg != "" {
				return msg
			}
			return "触发站点限流，请稍后再试"
		case companionapi.CodeUnsupported:
			if msg := strings.TrimSpace(ce.Message); msg != "" {
				return msg
			}
			return "暂不支持该链接格式，请粘贴 B 站、抖音、微博或 X 的内容链接"
		case companionapi.CodeBadRequest:
			if msg := strings.TrimSpace(ce.Message); msg != "" {
				return msg
			}
			return "未识别到有效链接，请检查后重试"
		case companionapi.CodeConflict:
			if msg := strings.TrimSpace(ce.Message); msg != "" {
				return msg
			}
			return "该内容已有进行中的缓存任务"
		case companionapi.CodeUnavailable:
			if msg := strings.TrimSpace(ce.Message); msg != "" {
				return msg
			}
			return "电脑端当前不可用，请确认电脑端 TRACE 正在运行"
		}
		if ce.Kind == companionclient.KindRateLimited {
			return "请求过于频繁，请稍后再试"
		}
		if strings.TrimSpace(ce.Message) != "" {
			return strings.TrimSpace(ce.Message)
		}
		return ce.UserMessage()
	}
	return err.Error()
}
