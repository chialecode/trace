package home

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/mediaview"
	"github.com/chialecode/trace/internal/companionapi"
)

func collectFeedViewerImages(item companionapi.FeedItem) []mediaview.ImageItem {
	out := make([]mediaview.ImageItem, 0, len(item.Images)+len(item.NotePages))
	seen := make(map[string]bool)
	add := func(im mediaview.ImageItem) {
		if im.RelPath == "" || seen[im.RelPath] {
			return
		}
		seen[im.RelPath] = true
		out = append(out, im)
	}
	for _, np := range item.NotePages {
		if np.Image != "" {
			add(mediaview.ImageItem{
				ItemKey:            item.ItemKey,
				RelPath:            np.Image,
				NoWatermarkRelPath: np.NoWatermark,
				MotionRelPath:      np.Motion,
				GIF:                np.GIF,
				Width:              np.Width,
				Height:             np.Height,
			})
		}
	}
	for _, im := range item.Images {
		add(mediaview.ImageItem{
			ItemKey:            item.ItemKey,
			RelPath:            im.Path,
			NoWatermarkRelPath: im.NoWatermark,
			MotionRelPath:      im.Motion,
			GIF:                im.GIF,
			Width:              im.Width,
			Height:             im.Height,
		})
	}
	return out
}

func buildFeedVideoRequest(item companionapi.FeedItem) (mediaview.VideoRequest, bool) {
	title := firstNonEmpty(item.Title, item.Text, "视频")

	// 1. Live recording with multi-segment sequential playback.
	if item.Live != nil && len(item.Live.Segments) > 0 {
		tracks := make([]mediaview.VideoTrack, 0, len(item.Live.Segments))
		for i, seg := range item.Live.Segments {
			if seg.File == "" {
				continue
			}
			spec := strings.TrimSpace(strings.Join(nonEmptyStrings(seg.QualityName, seg.Codec), " · "))
			if spec == "" {
				spec = item.QualitySummary
			}
			tracks = append(tracks, mediaview.VideoTrack{
				Label:          fmt.Sprintf("第 %d 段", i+1),
				RelPath:        seg.File,
				DurationSec:    int(seg.DurationSec),
				CodecHint:      firstNonEmpty(seg.Codec, item.Live.Codec, item.QualitySummary),
				Width:          seg.Width,
				Height:         seg.Height,
				QualitySummary: spec,
			})
		}
		if len(tracks) > 0 {
			return mediaview.VideoRequest{
				ItemKey:         item.ItemKey,
				Title:           firstNonEmpty(item.Title, "直播录像"),
				IsLiveRecording: true,
				Tracks:          tracks,
			}, true
		}
	}

	// 2. Multi-P video parts.
	if len(item.Parts) > 0 {
		tracks := make([]mediaview.VideoTrack, 0, len(item.Parts))
		for _, part := range item.Parts {
			if part.File == "" {
				continue
			}
			spec := strings.TrimSpace(strings.Join(nonEmptyStrings(part.QualityName, part.Codecs, part.AudioName), " · "))
			tracks = append(tracks, mediaview.VideoTrack{
				Label:          firstNonEmpty(part.Title, fmt.Sprintf("P%d", part.Page)),
				RelPath:        part.File,
				DurationSec:    part.Duration,
				CodecHint:      firstNonEmpty(part.Codecs, item.QualitySummary),
				AudioCodecHint: part.AudioName,
				Width:          part.Width,
				Height:         part.Height,
				QualitySummary: firstNonEmpty(spec, item.QualitySummary),
			})
		}
		if len(tracks) > 0 {
			return mediaview.VideoRequest{
				ItemKey: item.ItemKey,
				Title:   title,
				Tracks:  tracks,
			}, true
		}
	}

	// 3. Single video file or note video pages.
	if item.VideoRelPath != "" {
		return mediaview.VideoRequest{
			ItemKey: item.ItemKey,
			Title:   title,
			Tracks: []mediaview.VideoTrack{{
				Label:          title,
				RelPath:        item.VideoRelPath,
				DurationSec:    item.Duration,
				CodecHint:      item.QualitySummary,
				QualitySummary: item.QualitySummary,
			}},
		}, true
	}

	// 4. NotePages video entries.
	tracks := make([]mediaview.VideoTrack, 0)
	for i, np := range item.NotePages {
		if np.Video == "" {
			continue
		}
		codecHint := ""
		spec := ""
		w, h := np.Width, np.Height
		if np.VideoInfo != nil {
			codecHint = np.VideoInfo.Codec
			spec = strings.TrimSpace(strings.Join(nonEmptyStrings(np.VideoInfo.QualityName, np.VideoInfo.Codec), " · "))
			if w == 0 {
				w = np.VideoInfo.Width
			}
			if h == 0 {
				h = np.VideoInfo.Height
			}
		}
		tracks = append(tracks, mediaview.VideoTrack{
			Label:          fmt.Sprintf("视频 %d", i+1),
			RelPath:        np.Video,
			DurationSec:    np.Duration,
			CodecHint:      firstNonEmpty(codecHint, item.QualitySummary),
			Width:          w,
			Height:         h,
			QualitySummary: firstNonEmpty(spec, item.QualitySummary),
		})
	}
	if len(tracks) > 0 {
		return mediaview.VideoRequest{
			ItemKey: item.ItemKey,
			Title:   title,
			Tracks:  tracks,
		}, true
	}

	return mediaview.VideoRequest{}, false
}

func nonEmptyStrings(vals ...string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}
