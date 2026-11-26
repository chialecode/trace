package codeccheck

import (
	"fmt"
	"strings"
	"sync"
)

// Result describes the outcome of pre-checking a track's codec support.
type Result struct {
	Supported  bool
	Known      bool
	CodecLabel string
	IsAudio    bool
	Message    string
}

// ProbeFunc queries whether the device can decode the given MIME type at (width, height).
// Returning known=false means the platform has no native MediaCodecList query (e.g. desktop).
type ProbeFunc func(mime string, width, height int) (supported bool, known bool)

var (
	probeMu     sync.RWMutex
	customProbe ProbeFunc
)

// SetProbeForTest overrides the native codec probe (pass nil to restore default).
func SetProbeForTest(fn ProbeFunc) {
	probeMu.Lock()
	customProbe = fn
	probeMu.Unlock()
}

func runProbe(mime string, width, height int) (bool, bool) {
	probeMu.RLock()
	fn := customProbe
	probeMu.RUnlock()
	if fn != nil {
		return fn(mime, width, height)
	}
	ch, err := NewChecker()
	if err != nil {
		return true, false
	}
	defer func() { _ = ch.Close() }()
	ok, err := ch.CanDecode(mime, width, height)
	if err != nil {
		return true, false
	}
	return ok, true
}

// ParseCodecHints extracts normalized video and audio MIME types + display labels
// from free-form codec or quality summary strings (e.g. "1080P 高码率 · AV1 · 杜比全景声",
// "av01.0.08M.08", "hvc1", "hevc", "avc1", "flac").
func ParseCodecHints(hints ...string) (videoMIME, videoLabel, audioMIME, audioLabel string) {
	joined := strings.ToLower(strings.Join(hints, " "))
	if joined == "" {
		return "", "", "", ""
	}
	tokens := splitCodecTokens(joined)
	for _, tok := range tokens {
		if videoMIME == "" {
			switch {
			case tok == "av1" || tok == "av01" || strings.HasPrefix(tok, "av01."):
				videoMIME, videoLabel = "video/av01", "AV1"
			case tok == "hevc" || tok == "h265" || tok == "h.265" || tok == "hvc1" || tok == "hev1" || strings.HasPrefix(tok, "hvc1.") || strings.HasPrefix(tok, "hev1."):
				videoMIME, videoLabel = "video/hevc", "HEVC"
			case tok == "avc" || tok == "h264" || tok == "h.264" || tok == "avc1" || strings.HasPrefix(tok, "avc1."):
				videoMIME, videoLabel = "video/avc", "AVC"
			case tok == "vp9" || tok == "vp09" || strings.HasPrefix(tok, "vp09."):
				videoMIME, videoLabel = "video/x-vnd.on2.vp9", "VP9"
			}
		}
		if audioMIME == "" {
			switch {
			case tok == "flac" || strings.Contains(tok, "hi-res"):
				audioMIME, audioLabel = "audio/flac", "FLAC"
			case tok == "eac3" || tok == "ec-3" || tok == "dolby" || strings.Contains(tok, "杜比"):
				audioMIME, audioLabel = "audio/eac3", "杜比音轨"
			case tok == "ac3" || tok == "ac-3":
				audioMIME, audioLabel = "audio/ac3", "AC-3"
			}
		}
	}
	return videoMIME, videoLabel, audioMIME, audioLabel
}

func splitCodecTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ',', ';', '/', '|', '·', '(', ')', '[', ']', '（', '）':
			return true
		default:
			return false
		}
	})
}

// CheckTrack runs Layer-1 (MediaCodecList pre-check) on video and audio codec hints.
// If the device explicitly reports it cannot decode the track's video or audio codec,
// Result.Known is true, Result.Supported is false, and Result.Message gives a clear
// Chinese explanation (FR-MOB-014).
func CheckTrack(videoCodecHint, audioCodecHint string, width, height int) Result {
	vMIME, vLabel, aMIME, aLabel := ParseCodecHints(videoCodecHint, audioCodecHint)
	if vMIME != "" {
		ok, known := runProbe(vMIME, width, height)
		if known && !ok {
			return Result{
				Supported:  false,
				Known:      true,
				CodecLabel: vLabel,
				IsAudio:    false,
				Message:    UnsupportedMessage(vLabel, false),
			}
		}
		if known {
			if aMIME != "" {
				aOK, aKnown := runProbe(aMIME, 0, 0)
				if aKnown && !aOK {
					return Result{
						Supported:  false,
						Known:      true,
						CodecLabel: aLabel,
						IsAudio:    true,
						Message:    UnsupportedMessage(aLabel, true),
					}
				}
			}
			return Result{Supported: true, Known: true, CodecLabel: vLabel}
		}
	}
	if aMIME != "" {
		aOK, aKnown := runProbe(aMIME, 0, 0)
		if aKnown && !aOK {
			return Result{
				Supported:  false,
				Known:      true,
				CodecLabel: aLabel,
				IsAudio:    true,
				Message:    UnsupportedMessage(aLabel, true),
			}
		}
		if aKnown {
			return Result{Supported: true, Known: true, CodecLabel: aLabel, IsAudio: true}
		}
	}
	return Result{Supported: true, Known: false, CodecLabel: vLabel}
}

// UnsupportedMessage returns the user-facing Chinese explanation when a codec
// cannot be decoded on this phone (docs/ui.md §3.12, FR-MOB-014).
func UnsupportedMessage(codecLabel string, isAudio bool) string {
	label := strings.TrimSpace(codecLabel)
	if label == "" {
		return "这台手机不能播放这段视频的编码格式"
	}
	if isAudio {
		if strings.HasSuffix(label, "音轨") {
			return fmt.Sprintf("这台手机不能播放 %s", label)
		}
		return fmt.Sprintf("这台手机不能播放 %s 音轨", label)
	}
	return fmt.Sprintf("这台手机不能播放 %s 视频", label)
}
