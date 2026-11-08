package library

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ItemMetadata is the canonical on-disk schema for item.json (INV-02).
// It is backward-compatible with earlier TRACE item.json files in trace-data/library/.
type ItemMetadata struct {
	Version          int                `json:"version"`
	ID               string             `json:"id"`
	Site             string             `json:"site"`
	Kind             string             `json:"kind"`   // "video", "dynamic", "live"
	Status           string             `json:"status"` // "complete", "partial", "missing_files", "recording", "failed"
	Title            string             `json:"title"`
	Desc             string             `json:"desc,omitempty"`
	Text             string             `json:"text,omitempty"`
	DynamicType      string             `json:"dynamicType,omitempty"`
	TypeLabel        string             `json:"typeLabel,omitempty"`
	URL              string             `json:"url"`
	PubTs            int64              `json:"pubTs"`
	CachedAt         string             `json:"cachedAt"`
	Duration         int                `json:"duration,omitempty"`
	Author           ItemAuthor         `json:"author"`
	Media            []MediaFile        `json:"media,omitempty"`
	Parts            []VideoPartRecord  `json:"parts,omitempty"`
	ExpectedPages    []int              `json:"expectedPages,omitempty"`
	PendingParts     []string           `json:"pendingParts,omitempty"`
	Live             *LiveRecordingInfo `json:"live,omitempty"`
	Videos           []string           `json:"videos,omitempty"`
	Missing          []MissingRecord    `json:"missing,omitempty"`
	DirectCached     bool               `json:"directCached,omitempty"`
	UpgradeCheckedAt string             `json:"upgradeCheckedAt,omitempty"`
	UpgradeAvailable bool               `json:"upgradeAvailable,omitempty"`
	UpgradeHint      string             `json:"upgradeHint,omitempty"`

	// Runtime-populated fields
	ItemKey    string `json:"itemKey,omitempty"`
	RawID      string `json:"rawId,omitempty"`
	ItemDir    string `json:"itemDir,omitempty"`
	TotalBytes int64  `json:"totalBytes,omitempty"`
}

type LiveRecordingInfo struct {
	RoomID          int64             `json:"roomId"`
	LiveSince       int64             `json:"liveSince"`
	StartedAt       string            `json:"recordStartedAt"`
	EndedAt         string            `json:"recordEndedAt,omitempty"`
	EndTitle        string            `json:"endTitle,omitempty"`
	StopReason      string            `json:"stopReason,omitempty"`
	Source          string            `json:"source"`
	LastSequence    int64             `json:"lastSequence"`
	HasSequence     bool              `json:"hasSequence"`
	PendingFile     string            `json:"pendingFile,omitempty"`
	PendingBytes    int64             `json:"pendingBytes,omitempty"`
	PendingDuration float64           `json:"pendingDuration,omitempty"`
	Codec           string            `json:"codec,omitempty"`
	Quality         int               `json:"quality,omitempty"`
	QualityName     string            `json:"qualityName,omitempty"`
	Segments        []LiveSegmentInfo `json:"segments,omitempty"`
}

type LiveSegmentInfo struct {
	File        string  `json:"file"`
	Bytes       int64   `json:"bytes"`
	SHA256      string  `json:"sha256"`
	DurationSec float64 `json:"durationSec"`
	Codec       string  `json:"codec"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Quality     int     `json:"quality"`
	QualityName string  `json:"qualityName,omitempty"`
}

// MissingRecord describes a missing media component and the reason.
type MissingRecord struct {
	Item   string `json:"item"`
	Reason string `json:"reason"`
}

type ItemAuthor struct {
	Mid         int64  `json:"mid"`
	Name        string `json:"name"`
	Face        string `json:"face"`
	LatestPubTs int64  `json:"latestPubTs,omitempty"`
}

type MediaFile struct {
	Role     string `json:"role"` // "cover", "video", "audio", "danmaku", "subtitle", "image", "motion", "live_segment"
	Path     string `json:"path"` // relative to item directory
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256,omitempty"`
	Source   string `json:"source,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Duration int    `json:"duration,omitempty"`
	// Motion is the motion clip of a live photo (实况), relative to the item
	// directory; set on the still image.
	Motion string `json:"motion,omitempty"`
}

type VideoPartRecord struct {
	CID            int64              `json:"cid"`
	Page           int                `json:"page"`
	Title          string             `json:"title"`
	Duration       int                `json:"duration"`
	File           string             `json:"file"`
	Danmaku        string             `json:"danmaku,omitempty"`
	Subtitles      []string           `json:"subtitles,omitempty"`
	DanmakuState   string             `json:"danmakuState,omitempty"`
	SubtitleState  string             `json:"subtitleState,omitempty"`
	SubtitleTracks []PartSubtitleInfo `json:"subtitleTracks,omitempty"`
	Video          PartVideoInfo      `json:"video"`
	Audio          PartAudioInfo      `json:"audio"`
	Available      PartAvailableInfo  `json:"available"`
}

type PartSubtitleInfo struct {
	Language string `json:"lan"`
	Name     string `json:"lanDoc"`
	AI       bool   `json:"ai"`
	VTT      string `json:"vtt"`
	SRT      string `json:"srt"`
}

type PartVideoInfo struct {
	Quality     int    `json:"quality"`
	QualityName string `json:"qualityName"`
	CodecID     int    `json:"codecid"`
	Codecs      string `json:"codecs"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	FrameRate   string `json:"frameRate,omitempty"`
}

type PartAudioInfo struct {
	Quality int    `json:"quality"`
	Name    string `json:"name"`
	SavedAs string `json:"savedAs"`
}

type PartAvailableInfo struct {
	AcceptQuality []int    `json:"acceptQuality,omitempty"`
	Formats       []string `json:"formats,omitempty"`
	Returned      []int    `json:"returned,omitempty"`
	BestVideo     int      `json:"bestVideo,omitempty"`
	BestAudio     int      `json:"bestAudio,omitempty"`
	HasDolby      bool     `json:"hasDolby,omitempty"`
	HasFlac       bool     `json:"hasFlac,omitempty"`
	ProbedAt      string   `json:"probedAt,omitempty"`
	Downgraded    bool     `json:"downgraded,omitempty"`
	DowngradeHint string   `json:"downgradeHint,omitempty"`
}

// DotState represents the computed cache status indicator dot for an item (FR-CACHE-004).
type DotState struct {
	Key      string          `json:"key"`
	Dot      string          `json:"dot"`      // "none", "ring", "green", "yellow", "red"
	Label    string          `json:"label"`    // Accessible human-readable label
	Progress float64         `json:"progress"` // 0..1 when ring
	Missing  []MissingRecord `json:"missing,omitempty"`
	Upgrade  string          `json:"upgrade,omitempty"`
}

// DeletePlan describes the exact files and items that PlanDelete / Delete will remove or preserve (FR-LIB-003, INV-09).
type DeletePlan struct {
	TargetKey             string   `json:"targetKey"`
	ItemKeys              []string `json:"itemKeys"`
	RegisteredFiles       []string `json:"registeredFiles"`
	PreservedSharedVideos []string `json:"preservedSharedVideos,omitempty"`
	PreservedUserFiles    []string `json:"preservedUserFiles,omitempty"`
}

// AddRootReport summarizes the result of AddLibraryRoot (FR-LIB-006).
type AddRootReport struct {
	Root    string `json:"root"`
	Scanned int    `json:"scanned"`
	Added   int    `json:"added"`
	Skipped int    `json:"skipped"`
}

// FeedCursor represents a keyset pagination cursor (pub_ts, key).
type FeedCursor struct {
	PubTs int64  `json:"pubTs"`
	Key   string `json:"key"`
}

// FeedPageRequest specifies bidirectional keyset pagination parameters for the home feed (FR-FEED-001).
type FeedPageRequest struct {
	Cursor     *FeedCursor `json:"cursor,omitempty"`
	Direction  string      `json:"direction"` // "older" or "newer"
	Order      string      `json:"order"`     // "newest" or "oldest"
	Limit      int         `json:"limit"`
	Authors    []int64     `json:"authors,omitempty"`
	Kind       string      `json:"kind,omitempty"`
	CachedOnly bool        `json:"cachedOnly,omitempty"`
}

// FeedPageResult contains a keyset-paginated slice of feed items and next/prev cursors.
type FeedPageResult struct {
	Items      []FeedEntry `json:"items"`
	NextCursor *FeedCursor `json:"nextCursor,omitempty"`
	PrevCursor *FeedCursor `json:"prevCursor,omitempty"`
	HasMore    bool        `json:"hasMore"`
}

// FeedEntry represents an item in the unified feed (online discovered + locally cached).
type FeedEntry struct {
	FeedKey          string `json:"feedKey"`
	Site             string `json:"site"`
	Kind             string `json:"kind"` // "video", "dynamic", "live"
	RawID            string `json:"rawId"`
	Mid              int64  `json:"mid"`
	AuthorName       string `json:"authorName"`
	AuthorFace       string `json:"authorFace"`
	Title            string `json:"title"`
	Summary          string `json:"summary"`
	CoverURL         string `json:"coverUrl"`
	CanonicalURL     string `json:"canonicalUrl"`
	PubTs            int64  `json:"pubTs"`
	DiscoveredAt     string `json:"discoveredAt"`
	Cached           bool   `json:"cached"`
	CacheStatus      string `json:"cacheStatus,omitempty"`
	ItemKey          string `json:"itemKey,omitempty"`
	ItemDir          string `json:"itemDir,omitempty"`
	UpgradeAvailable bool   `json:"upgradeAvailable,omitempty"`
	UpgradeHint      string `json:"upgradeHint,omitempty"`
}

// FeedFilter specifies filtering criteria for querying the unified feed or local library.
type FeedFilter struct {
	Kind       string  `json:"kind"` // "all", "video", "dynamic", "live"
	Mid        int64   `json:"mid"`  // 0 means all creators
	Authors    []int64 `json:"authors,omitempty"`
	CachedOnly bool    `json:"cachedOnly"` // true = local library view
	Keyword    string  `json:"keyword"`
	Sort       string  `json:"sort,omitempty"` // "newest", "oldest"
	Limit      int     `json:"limit"`
	Offset     int     `json:"offset"`
}

// ParseItemMetadata parses an item.json payload, tolerating both string and numeric timestamps/IDs from older versions.
func ParseItemMetadata(raw []byte) (ItemMetadata, error) {
	var flex struct {
		ItemMetadata
		RawAuthor struct {
			Mid  any    `json:"mid"`
			Name string `json:"name"`
			Face string `json:"face"`
		} `json:"author"`
		RawVideos           []any `json:"videos"`
		RawPubTs            any   `json:"pubTs"`
		RawCachedAt         any   `json:"cachedAt"`
		RawUpgradeCheckedAt any   `json:"upgradeCheckedAt"`
	}
	if err := json.Unmarshal(raw, &flex); err != nil {
		return ItemMetadata{}, err
	}
	meta := flex.ItemMetadata
	if meta.Version <= 0 {
		meta.Version = 1
	}
	if meta.Site == "" {
		meta.Site = "bilibili"
	}
	meta.Author.Mid = parseFlexibleUnix(flex.RawAuthor.Mid)
	meta.Author.Name = flex.RawAuthor.Name
	meta.Author.Face = flex.RawAuthor.Face
	meta.PubTs = parseFlexibleUnix(flex.RawPubTs)
	meta.CachedAt = parseFlexibleTimestamp(flex.RawCachedAt)
	meta.UpgradeCheckedAt = parseFlexibleTimestamp(flex.RawUpgradeCheckedAt)
	if len(flex.RawVideos) > 0 {
		videos := make([]string, 0, len(flex.RawVideos))
		for _, v := range flex.RawVideos {
			switch tv := v.(type) {
			case string:
				if strings.TrimSpace(tv) != "" {
					videos = append(videos, strings.TrimSpace(tv))
				}
			case map[string]any:
				if bvid, ok := tv["bvid"].(string); ok && strings.TrimSpace(bvid) != "" {
					videos = append(videos, strings.TrimSpace(bvid))
				} else if id, ok := tv["id"].(string); ok && strings.TrimSpace(id) != "" {
					videos = append(videos, strings.TrimSpace(id))
				}
			}
		}
		meta.Videos = videos
	}
	if meta.Kind == "" || meta.ID == "" {
		return ItemMetadata{}, fmt.Errorf("library: item.json missing required kind or id")
	}
	meta.RawID = NormalizeRawID(meta.Kind, meta.ID)
	meta.ItemKey = CanonicalItemKey(meta.Site, meta.Kind, meta.RawID)
	if meta.Status == "" {
		meta.Status = "complete"
	}
	return meta, nil
}

// ComputeDotState computes the canonical status dot for an ItemMetadata (FR-CACHE-004, INV-06).
func ComputeDotState(meta ItemMetadata) DotState {
	d := DotState{
		Key:     meta.ItemKey,
		Missing: meta.Missing,
		Upgrade: meta.UpgradeHint,
	}
	switch meta.Status {
	case "recording", "running", "queued", "downloading", "muxing":
		d.Dot = "ring"
		d.Label = "进行中"
	case "failed", "missing_files", "corrupt":
		d.Dot = "red"
		if meta.Status == "missing_files" {
			d.Label = "文件缺失"
		} else {
			d.Label = "缓存失败"
		}
	case "partial", "paused":
		d.Dot = "yellow"
		if meta.Status == "paused" {
			d.Label = "已暂停"
		} else {
			d.Label = "部分缓存"
		}
	case "complete":
		incomplete := false
		for _, page := range meta.ExpectedPages {
			found := false
			for _, part := range meta.Parts {
				if part.Page == page && part.File != "" {
					found = true
				}
			}
			incomplete = incomplete || !found
		}
		belowBest := false
		for _, part := range meta.Parts {
			belowBest = belowBest || part.Available.BestVideo > part.Video.Quality || audioPriority(part.Available.BestAudio) > audioPriority(part.Audio.Quality)
		}
		if meta.UpgradeAvailable || len(meta.Missing) > 0 || incomplete || belowBest {
			d.Dot = "yellow"
			if meta.UpgradeAvailable {
				d.Label = "可升级画质"
			} else {
				d.Label = "部分缺失"
			}
		} else {
			d.Dot = "green"
			d.Label = "已缓存"
		}
	default:
		d.Dot = "none"
		d.Label = "未缓存"
	}
	return d
}

func parseFlexibleUnix(v any) int64 {
	switch val := v.(type) {
	case float64:
		return int64(val)
	case int64:
		return val
	case string:
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			return n
		}
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func parseFlexibleTimestamp(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val <= 0 {
			return ""
		}
		return time.Unix(int64(val), 0).UTC().Format(time.RFC3339)
	case int64:
		if val <= 0 {
			return ""
		}
		return time.Unix(val, 0).UTC().Format(time.RFC3339)
	}
	return ""
}
