package mediaview

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDanmakuXMLParseLayoutModesDensityAndSeek(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<i>
  <d p="1.5,1,25,16777215,1700000000,0,abc,101">第一条滚动弹幕</d>
  <d p="2.0,5,25,16711680,1700000000,0,abc,102">顶部固定弹幕</d>
  <d p="2.5,4,25,65280,1700000000,0,abc,103">底部固定弹幕</d>
  <d p="2.6,1,25,16777215,1700000000,0,abc,104">同秒滚动2</d>
  <d p="2.7,1,25,16777215,1700000000,0,abc,105">同秒滚动3</d>
  <d p="2.8,1,25,16777215,1700000000,0,abc,106">同秒滚动4</d>
  <d p="bad,1,25,16777215">忽略无效时间</d>
</i>`

	eng, err := ParseDanmakuXML([]byte(xmlData))
	if err != nil {
		t.Fatalf("ParseDanmakuXML: %v", err)
	}
	if eng.Count() != 6 {
		t.Fatalf("Count = %d, want 6", eng.Count())
	}

	// At t=3.0s with unlimited density, all 6 items should be active (rolling + top + bottom).
	frame := eng.LayoutFrame(3.0, 412, 300, 1.0, 0.85, "unlimited")
	if len(frame) < 5 {
		t.Fatalf("expected >=5 visible danmaku at t=3.0, got %d", len(frame))
	}
	hasRoll, hasTop, hasBottom := false, false, false
	for _, dm := range frame {
		switch dm.Mode {
		case 1:
			hasRoll = true
		case 5:
			hasTop = true
		case 4:
			hasBottom = true
		}
		if dm.Color.A == 0 {
			t.Fatalf("expected non-zero alpha on %+v", dm)
		}
	}
	if !hasRoll || !hasTop || !hasBottom {
		t.Fatalf("expected rolling/top/bottom modes visible: roll=%v top=%v bottom=%v", hasRoll, hasTop, hasBottom)
	}

	// Low density limits second=2 entries to 2 per second.
	lowFrame := eng.LayoutFrame(3.0, 412, 300, 1.0, 0.5, "low")
	sec2Count := 0
	for _, dm := range lowFrame {
		if dm.TimeSec >= 2.0 && dm.TimeSec < 3.0 {
			sec2Count++
		}
	}
	if sec2Count > 2 {
		t.Fatalf("low density allowed %d entries in second 2, want <= 2", sec2Count)
	}

	// Seeking past all windows leaves zero stale danmaku.
	later := eng.LayoutFrame(30.0, 412, 300, 0.5, 0.85, "medium")
	if len(later) != 0 {
		t.Fatalf("expected 0 danmaku after seek to 30s, got %d", len(later))
	}
}

func TestDanmaku10KLayoutPerformance(t *testing.T) {
	items := make([]DanmakuItem, 10000)
	for i := range items {
		items[i] = DanmakuItem{
			ID:       fmt.Sprint(i),
			TimeSec:  float64(i) * 0.05,
			Mode:     1,
			FontSize: 15,
			ColorRGB: 0xFFFFFF,
			Text:     "性能测试弹幕",
		}
	}
	eng := NewDanmakuEngine(items)
	start := time.Now()
	for frame := 0; frame < 20; frame++ {
		_ = eng.LayoutFrame(float64(frame)*10.0+5.0, 412, 320, 0.5, 0.85, "medium")
	}
	avg := time.Since(start) / 20
	if avg > 10*time.Millisecond {
		t.Fatalf("average 10k danmaku frame layout took %v, expected fast sub-millisecond layout", avg)
	}
}

func TestSubtitleWebVTTAndSRTParsingAndActiveCue(t *testing.T) {
	vtt := "\uFEFFWEBVTT\n\nNOTE ignore comment\n\n00:01.000 --> 00:04.500 align:center\n第一句 &amp; &lt;测试&gt;\n\n00:03.000 --> 00:06.000\n重叠第二句\n"
	tr := NewSubtitleTrack("zh-CN", "中文（简体）", []byte(vtt))
	if len(tr.Cues) != 2 {
		t.Fatalf("expected 2 VTT cues, got %d", len(tr.Cues))
	}
	if got := tr.ActiveCueAt(0.5); got != "" {
		t.Fatalf("expected empty cue at 0.5s, got %q", got)
	}
	if got := tr.ActiveCueAt(2.0); got != "第一句 & <测试>" {
		t.Fatalf("cue at 2.0s = %q", got)
	}
	if got := tr.ActiveCueAt(3.5); !strings.Contains(got, "第一句") || !strings.Contains(got, "重叠第二句") {
		t.Fatalf("expected both overlapping cues at 3.5s, got %q", got)
	}
	if got := tr.ActiveCueAt(7.0); got != "" {
		t.Fatalf("expected empty cue at 7.0s, got %q", got)
	}

	srt := "1\r\n00:00:10,250 --> 00:00:12,750\r\nSRT 第一行\r\n第二行\r\n"
	srtTr := NewSubtitleTrack("en", "English", []byte(srt))
	if len(srtTr.Cues) != 1 {
		t.Fatalf("expected 1 SRT cue, got %d", len(srtTr.Cues))
	}
	if got := srtTr.ActiveCueAt(11.0); got != "SRT 第一行\n第二行" {
		t.Fatalf("SRT cue at 11.0s = %q", got)
	}
}
