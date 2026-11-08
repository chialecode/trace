package bilibili

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// QualityNameMap maps Bilibili qn codes to human-readable descriptions.
var QualityNameMap = map[int]string{
	127: "8K 超高清",
	126: "杜比视界",
	125: "真彩 HDR",
	120: "4K 超清",
	116: "1080P 60帧",
	112: "1080P 高码率",
	80:  "1080P 高清",
	74:  "720P 60帧",
	64:  "720P 高清",
	32:  "480P 清晰",
	16:  "360P 流畅",
}

// QualityName returns the label for a Bilibili video quality code.
func QualityName(qn int) string {
	if name, ok := QualityNameMap[qn]; ok {
		return name
	}
	return fmt.Sprintf("QN-%d", qn)
}

// QualityRank returns a strictly monotonic rank for comparing video quality codes.
func QualityRank(qn int) int {
	for i, quality := range []int{16, 32, 64, 74, 80, 112, 116, 120, 125, 126, 127} {
		if qn == quality {
			return i + 1
		}
	}
	return 0
}

// AudioQualityInfo returns the label and file extension for a Bilibili DASH audio stream ID.
func AudioQualityInfo(id int) (name string, ext string) {
	switch id {
	case 30251:
		return "Hi-Res 无损", ".m4a"
	case 30250:
		return "杜比全景声", ".m4a"
	case 30280:
		return "192K 高音质", ".m4a"
	case 30232:
		return "132K 标准", ".m4a"
	case 30216:
		return "64K 流畅", ".m4a"
	default:
		return fmt.Sprintf("Audio-%d", id), ".m4a"
	}
}

// PlaySelectionOptions specifies user preferences for selecting DASH video and audio tracks.
type PlaySelectionOptions struct {
	PreferredCodec      string // "avc", "hevc", "av1"
	CodecOrder          []string
	PreferLosslessAudio bool
	PreferDolbyAudio    bool
	TargetQn            int // 0 or 127 means highest available
}

// SelectedVideoStream holds the chosen video stream metadata and ordered candidate URLs.
type SelectedVideoStream struct {
	Quality     int      `json:"quality"`
	QualityName string   `json:"qualityName"`
	CodecID     int      `json:"codecid"`
	CodecName   string   `json:"codecName"`
	Codecs      string   `json:"codecs"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	FrameRate   string   `json:"frameRate"`
	Bandwidth   int      `json:"bandwidth"`
	URLs        []string `json:"-"`
}

// SelectedAudioStream holds the chosen audio stream metadata and ordered candidate URLs.
type SelectedAudioStream struct {
	Quality   int      `json:"quality"`
	Name      string   `json:"name"`
	Codecs    string   `json:"codecs"`
	SavedExt  string   `json:"savedExt"`
	Bandwidth int      `json:"bandwidth"`
	URLs      []string `json:"-"`
}

// AvailableStreams records all qualities returned vs accepted by the platform for item.json and upgrade checks (C21, C27).
type AvailableStreams struct {
	AcceptQuality []int    `json:"acceptQuality"`
	Formats       []string `json:"formats"`
	Returned      []int    `json:"returned"`
	BestVideo     int      `json:"bestVideo"`
	BestAudio     int      `json:"bestAudio"`
	HasDolby      bool     `json:"hasDolby"`
	HasFlac       bool     `json:"hasFlac"`
	ProbedAt      string   `json:"probedAt"`
	Downgraded    bool     `json:"downgraded,omitempty"`
	DowngradeHint string   `json:"downgradeHint,omitempty"`
}

// PlayPlan contains the selected video/audio streams and full availability diagnostics.
type PlayPlan struct {
	Mode          string               `json:"mode"` // "dash" or "durl"
	Duration      float64              `json:"duration,omitempty"`
	Video         SelectedVideoStream  `json:"video"`
	Audio         *SelectedAudioStream `json:"audio,omitempty"`
	FallbackAudio *SelectedAudioStream `json:"fallbackAudio,omitempty"`
	Available     AvailableStreams     `json:"available"`
}

type rawDashTrack struct {
	ID        int      `json:"id"`
	BaseURL   string   `json:"baseUrl"`
	BaseURL2  string   `json:"base_url"`
	BackupURL []string `json:"backupUrl"`
	Backup2   []string `json:"backup_url"`
	Bandwidth int      `json:"bandwidth"`
	MimeType  string   `json:"mimeType"`
	Codecs    string   `json:"codecs"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	FrameRate string   `json:"frameRate"`
	Frame2    string   `json:"frame_rate"`
	CodecID   int      `json:"codecid"`
}

// PlayURL fetches /x/player/wbi/playurl and builds a PlayPlan according to opts.
func (c *Client) PlayURL(ctx context.Context, bvid string, cid int64, opts PlaySelectionOptions) (PlayPlan, error) {
	if err := c.RequireAuth("/x/player/wbi/playurl"); err != nil {
		return PlayPlan{}, err
	}
	if bvid == "" || cid <= 0 {
		return PlayPlan{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "获取播放地址需要有效的 bvid 与 cid",
		}
	}

	targetQn := opts.TargetQn
	if targetQn <= 0 {
		targetQn = 127
	}

	params := url.Values{}
	params.Set("bvid", bvid)
	params.Set("cid", strconv.FormatInt(cid, 10))
	params.Set("qn", strconv.Itoa(targetQn))
	params.Set("fnval", "4048")
	params.Set("fnver", "0")
	params.Set("fourk", "1")

	var raw struct {
		IsPreview         bool     `json:"is_preview"`
		TimeLength        int64    `json:"timelength"`
		Quality           int      `json:"quality"`
		Format            string   `json:"format"`
		AcceptFormat      string   `json:"accept_format"`
		AcceptDescription []string `json:"accept_description"`
		AcceptQuality     []int    `json:"accept_quality"`
		Dash              *struct {
			Duration int            `json:"duration"`
			Video    []rawDashTrack `json:"video"`
			Audio    []rawDashTrack `json:"audio"`
			Dolby    *struct {
				Type  int            `json:"type"`
				Audio []rawDashTrack `json:"audio"`
			} `json:"dolby"`
			Flac *struct {
				Display bool          `json:"display"`
				Audio   *rawDashTrack `json:"audio"`
			} `json:"flac"`
		} `json:"dash"`
		Durl []struct {
			Order     int      `json:"order"`
			Length    int      `json:"length"`
			Size      int64    `json:"size"`
			URL       string   `json:"url"`
			BackupURL []string `json:"backup_url"`
		} `json:"durl"`
	}

	if err := c.doWBIGet(ctx, "/x/player/wbi/playurl", params, &raw); err != nil {
		return PlayPlan{}, err
	}
	if raw.IsPreview {
		return PlayPlan{}, &SiteError{Kind: ErrKindUnavailable, Message: "当前账号只能获得试看，不缓存试看片段"}
	}

	formats := []string{}
	if raw.AcceptFormat != "" {
		formats = strings.Split(raw.AcceptFormat, ",")
	}

	avail := AvailableStreams{
		AcceptQuality: raw.AcceptQuality,
		Formats:       formats,
		ProbedAt:      c.nowFunc().UTC().Format(time.RFC3339),
	}

	// Prefer DASH tracks when present.
	if raw.Dash != nil && len(raw.Dash.Video) > 0 {
		returnedSet := make(map[int]bool)
		bestVideoQn := 0
		for _, vt := range raw.Dash.Video {
			if !returnedSet[vt.ID] {
				returnedSet[vt.ID] = true
				avail.Returned = append(avail.Returned, vt.ID)
			}
			if QualityRank(vt.ID) > QualityRank(bestVideoQn) && trackPrimaryURL(vt) != "" {
				bestVideoQn = vt.ID
			}
		}
		sort.Sort(sort.Reverse(sort.IntSlice(avail.Returned)))
		avail.BestVideo = bestVideoQn

		hasFlac := raw.Dash.Flac != nil && raw.Dash.Flac.Audio != nil && trackPrimaryURL(*raw.Dash.Flac.Audio) != ""
		hasDolby := raw.Dash.Dolby != nil && len(raw.Dash.Dolby.Audio) > 0 && trackPrimaryURL(raw.Dash.Dolby.Audio[0]) != ""
		avail.HasFlac = hasFlac
		avail.HasDolby = hasDolby

		selectedVideo := selectDashVideoOrder(raw.Dash.Video, opts.PreferredCodec, opts.TargetQn, opts.CodecOrder)
		if len(selectedVideo.URLs) == 0 {
			return PlayPlan{}, &SiteError{Kind: ErrKindUnavailable, Message: "接口未返回可用的视频流"}
		}
		selectedAudio, bestAudioID := selectDashAudio(raw.Dash.Audio, raw.Dash.Flac, raw.Dash.Dolby, opts)
		var fallbackAudio *SelectedAudioStream
		if selectedAudio != nil && (selectedAudio.Quality == 30250 || selectedAudio.Quality == 30251) {
			fallbackAudio, _ = selectDashAudio(raw.Dash.Audio, nil, nil, PlaySelectionOptions{})
		}
		avail.BestAudio = bestAudioID

		maxAccept := 0
		for _, q := range raw.AcceptQuality {
			if QualityRank(q) > QualityRank(maxAccept) {
				maxAccept = q
			}
		}
		if QualityRank(maxAccept) > QualityRank(selectedVideo.Quality) {
			avail.Downgraded = true
			avail.DowngradeHint = fmt.Sprintf(
				"站点列表最高画质为 %s (%d)，当前账号权限实际可获取最高画质为 %s (%d)",
				QualityName(maxAccept), maxAccept,
				selectedVideo.QualityName, selectedVideo.Quality,
			)
		}

		return PlayPlan{
			Mode:          "dash",
			Duration:      float64(raw.Dash.Duration),
			Video:         selectedVideo,
			Audio:         selectedAudio,
			FallbackAudio: fallbackAudio,
			Available:     avail,
		}, nil
	}

	// Fallback to legacy single-file DURL stream.
	if len(raw.Durl) > 0 {
		if len(raw.Durl) != 1 {
			return PlayPlan{}, &SiteError{Kind: ErrKindBlocked, Message: "站点只返回了分段旧格式，无法保存为完整 MP4"}
		}
		var urls []string
		for _, d := range raw.Durl {
			if d.URL != "" {
				urls = append(urls, d.URL)
			}
			urls = append(urls, d.BackupURL...)
		}
		urls = prioritizeCDNURLs(urls)
		qn := raw.Quality
		if qn <= 0 {
			qn = 64
		}
		avail.Returned = []int{qn}
		avail.BestVideo = qn
		return PlayPlan{
			Mode:     "durl",
			Duration: float64(raw.TimeLength) / 1000,
			Video: SelectedVideoStream{
				Quality:     qn,
				QualityName: QualityName(qn),
				CodecID:     7,
				CodecName:   "avc",
				Codecs:      "avc1",
				URLs:        urls,
			},
			Available: avail,
		}, nil
	}

	return PlayPlan{}, &SiteError{
		Kind:     ErrKindUnavailable,
		Endpoint: "/x/player/wbi/playurl",
		Message:  "接口未返回可用的视频流地址",
	}
}

func selectDashVideoOrder(tracks []rawDashTrack, preferredCodec string, targetQn int, order []string) SelectedVideoStream {
	preferredCodec = strings.ToLower(strings.TrimSpace(preferredCodec))
	if preferredCodec == "" {
		preferredCodec = "avc"
	}

	// Find highest quality <= targetQn (or overall highest if targetQn <= 0 or none <= targetQn).
	bestQn := 0
	for _, t := range tracks {
		if trackPrimaryURL(t) == "" || targetQn > 0 && QualityRank(t.ID) > QualityRank(targetQn) {
			continue
		}
		if QualityRank(t.ID) > QualityRank(bestQn) {
			bestQn = t.ID
		}
	}
	if bestQn == 0 {
		for _, t := range tracks {
			if trackPrimaryURL(t) != "" && QualityRank(t.ID) > QualityRank(bestQn) {
				bestQn = t.ID
			}
		}
	}

	var candidates []rawDashTrack
	for _, t := range tracks {
		if t.ID == bestQn && trackPrimaryURL(t) != "" {
			candidates = append(candidates, t)
		}
	}

	// Sort candidates by codec preference, then bandwidth descending.
	sort.SliceStable(candidates, func(i, j int) bool {
		ci := codecOrderScore(candidates[i], preferredCodec, order)
		cj := codecOrderScore(candidates[j], preferredCodec, order)
		if ci != cj {
			return ci > cj
		}
		return candidates[i].Bandwidth > candidates[j].Bandwidth
	})

	if len(candidates) == 0 {
		return SelectedVideoStream{}
	}
	chosen := candidates[0]
	fr := chosen.FrameRate
	if fr == "" {
		fr = chosen.Frame2
	}
	return SelectedVideoStream{
		Quality:     chosen.ID,
		QualityName: QualityName(chosen.ID),
		CodecID:     chosen.CodecID,
		CodecName:   codecNameFromTrack(chosen),
		Codecs:      chosen.Codecs,
		Width:       chosen.Width,
		Height:      chosen.Height,
		FrameRate:   fr,
		Bandwidth:   chosen.Bandwidth,
		URLs:        collectTrackURLs(chosen),
	}
}

func selectDashAudio(
	normal []rawDashTrack,
	flac *struct {
		Display bool          `json:"display"`
		Audio   *rawDashTrack `json:"audio"`
	},
	dolby *struct {
		Type  int            `json:"type"`
		Audio []rawDashTrack `json:"audio"`
	},
	opts PlaySelectionOptions,
) (*SelectedAudioStream, int) {
	bestID := 0
	if flac != nil && flac.Audio != nil && trackPrimaryURL(*flac.Audio) != "" {
		bestID = 30251
		if opts.PreferLosslessAudio {
			t := *flac.Audio
			if t.ID == 0 {
				t.ID = 30251
			}
			name, ext := AudioQualityInfo(t.ID)
			return &SelectedAudioStream{
				Quality:   t.ID,
				Name:      name,
				Codecs:    firstNonEmpty(t.Codecs, "flac"),
				SavedExt:  ext,
				Bandwidth: t.Bandwidth,
				URLs:      collectTrackURLs(t),
			}, bestID
		}
	}

	if dolby != nil && len(dolby.Audio) > 0 && trackPrimaryURL(dolby.Audio[0]) != "" {
		if bestID == 0 {
			bestID = 30250
		}
		if opts.PreferDolbyAudio {
			t := dolby.Audio[0]
			if t.ID == 0 {
				t.ID = 30250
			}
			name, ext := AudioQualityInfo(t.ID)
			return &SelectedAudioStream{
				Quality:   t.ID,
				Name:      name,
				Codecs:    firstNonEmpty(t.Codecs, "ec-3"),
				SavedExt:  ext,
				Bandwidth: t.Bandwidth,
				URLs:      collectTrackURLs(t),
			}, bestID
		}
	}

	if len(normal) == 0 {
		return nil, bestID
	}

	sorted := append([]rawDashTrack(nil), normal...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if audioRank(sorted[i].ID) != audioRank(sorted[j].ID) {
			return audioRank(sorted[i].ID) > audioRank(sorted[j].ID)
		}
		return sorted[i].Bandwidth > sorted[j].Bandwidth
	})

	chosen := sorted[0]
	if bestID == 0 {
		bestID = chosen.ID
	}
	name, ext := AudioQualityInfo(chosen.ID)
	return &SelectedAudioStream{
		Quality:   chosen.ID,
		Name:      name,
		Codecs:    firstNonEmpty(chosen.Codecs, "mp4a.40.2"),
		SavedExt:  ext,
		Bandwidth: chosen.Bandwidth,
		URLs:      collectTrackURLs(chosen),
	}, bestID
}

func audioRank(id int) int {
	switch id {
	case 30251:
		return 5
	case 30250:
		return 4
	case 30280:
		return 3
	case 30232:
		return 2
	case 30216:
		return 1
	default:
		return 0
	}
}

func codecNameFromTrack(t rawDashTrack) string {
	switch t.CodecID {
	case 7:
		return "avc"
	case 12:
		return "hevc"
	case 13:
		return "av1"
	}
	cl := strings.ToLower(t.Codecs)
	switch {
	case strings.HasPrefix(cl, "avc"):
		return "avc"
	case strings.HasPrefix(cl, "hev"), strings.HasPrefix(cl, "hvc"):
		return "hevc"
	case strings.HasPrefix(cl, "av01"):
		return "av1"
	default:
		return "avc"
	}
}

func codecOrderScore(t rawDashTrack, preferred string, order []string) int {
	if len(order) == 0 {
		order = []string{preferred, "avc", "av1", "hevc"}
	}
	for i, codec := range order {
		if codecNameFromTrack(t) == codec {
			return len(order) - i
		}
	}
	return 0
}

func trackPrimaryURL(t rawDashTrack) string {
	if t.BaseURL != "" {
		return t.BaseURL
	}
	return t.BaseURL2
}

func collectTrackURLs(t rawDashTrack) []string {
	var raw []string
	if t.BaseURL != "" {
		raw = append(raw, t.BaseURL)
	}
	if t.BaseURL2 != "" && t.BaseURL2 != t.BaseURL {
		raw = append(raw, t.BaseURL2)
	}
	raw = append(raw, t.BackupURL...)
	raw = append(raw, t.Backup2...)
	return prioritizeCDNURLs(raw)
}

// prioritizeCDNURLs deduplicates URLs and puts official CDN hosts before *.mcdn.* P2P/edge nodes.
func prioritizeCDNURLs(urls []string) []string {
	seen := make(map[string]bool, len(urls))
	var official []string
	var mcdn []string
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		if isMCDNHost(u) {
			mcdn = append(mcdn, u)
		} else {
			official = append(official, u)
		}
	}
	return append(official, mcdn...)
}

func isMCDNHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.Contains(host, ".mcdn.") || strings.HasPrefix(host, "mcdn.")
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
