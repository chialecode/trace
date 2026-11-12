package library

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CurrentSchemaVersion is the SQLite PRAGMA user_version written to library.db.
const CurrentSchemaVersion = 2

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
	DouyinVideo      *DouyinVideoInfo   `json:"douyinVideo,omitempty"`
	NotePages        []NotePageRecord   `json:"notePages,omitempty"`
	Music            *MusicInfo         `json:"music,omitempty"`
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
	RoomIDStr       string            `json:"roomIdStr,omitempty"`
	WebRID          string            `json:"webRid,omitempty"`
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

// AuthorRef identifies a creator by (site, uid) across Bilibili (decimal mid) and Douyin (sec_uid).
type AuthorRef struct {
	Site string `json:"site"`
	UID  string `json:"uid"`
}

type ItemAuthor struct {
	Site        string `json:"site,omitempty"`
	UID         string `json:"uid,omitempty"`
	UniqueID    string `json:"uniqueId,omitempty"`
	Mid         int64  `json:"mid"`
	Name        string `json:"name"`
	Face        string `json:"face"`
	LatestPubTs int64  `json:"latestPubTs,omitempty"`
}

// DouyinVideoInfo records Douyin single-file video gear/stream metadata and available gears.
type DouyinVideoInfo struct {
	File           string   `json:"file,omitempty"`
	Gear           string   `json:"gear,omitempty"`
	QualityName    string   `json:"qualityName,omitempty"`
	Codec          string   `json:"codec,omitempty"`
	Width          int      `json:"width,omitempty"`
	Height         int      `json:"height,omitempty"`
	FPS            int      `json:"fps,omitempty"`
	Bitrate        int64    `json:"bitrate,omitempty"`
	AvailableGears []string `json:"availableGears,omitempty"`
}

// NotePageRecord records one ordered page of a Douyin image/mixed note (image, motion live-photo, video, or unknown).
type NotePageRecord struct {
	Index       int              `json:"index"`
	Kind        string           `json:"kind"` // "image", "motion", "video", "unknown"
	UnknownKind string           `json:"unknownKind,omitempty"`
	Image       string           `json:"image,omitempty"`
	Cover       string           `json:"cover,omitempty"`
	Motion      string           `json:"motion,omitempty"`
	Video       string           `json:"video,omitempty"`
	Width       int              `json:"width,omitempty"`
	Height      int              `json:"height,omitempty"`
	Duration    int              `json:"duration,omitempty"`
	VideoInfo   *DouyinVideoInfo `json:"videoInfo,omitempty"`
}

// MusicInfo records the background music track of a Douyin note.
type MusicInfo struct {
	ID       string `json:"id,omitempty"`
	Title    string `json:"title,omitempty"`
	Author   string `json:"author,omitempty"`
	Duration int    `json:"duration,omitempty"`
	File     string `json:"file,omitempty"`
	Cover    string `json:"cover,omitempty"`
	Original bool   `json:"original,omitempty"`
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
	Gear        string `json:"gear,omitempty"`
	CodecID     int    `json:"codecid"`
	Codecs      string `json:"codecs"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	FrameRate   string `json:"frameRate,omitempty"`
	Bitrate     int64  `json:"bitrate,omitempty"`
}

type PartAudioInfo struct {
	Quality int    `json:"quality"`
	Name    string `json:"name"`
	SavedAs string `json:"savedAs"`
}

type PartAvailableInfo struct {
	AcceptQuality  []int    `json:"acceptQuality,omitempty"`
	AvailableGears []string `json:"availableGears,omitempty"`
	Formats        []string `json:"formats,omitempty"`
	Returned       []int    `json:"returned,omitempty"`
	BestVideo      int      `json:"bestVideo,omitempty"`
	BestAudio      int      `json:"bestAudio,omitempty"`
	HasDolby       bool     `json:"hasDolby,omitempty"`
	HasFlac        bool     `json:"hasFlac,omitempty"`
	ProbedAt       string   `json:"probedAt,omitempty"`
	Downgraded     bool     `json:"downgraded,omitempty"`
	DowngradeHint  string   `json:"downgradeHint,omitempty"`
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
	Site       string      `json:"site,omitempty"`
	Authors    []int64     `json:"authors,omitempty"`
	AuthorRefs []AuthorRef `json:"authorRefs,omitempty"`
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
	AuthorUID        string `json:"authorUid,omitempty"`
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
	Site       string      `json:"site,omitempty"` // "all"/"", "bilibili", "douyin"
	Kind       string      `json:"kind"`           // "all", "video", "dynamic", "live"
	Mid        int64       `json:"mid"`            // 0 means all creators
	AuthorUID  string      `json:"authorUid,omitempty"`
	Authors    []int64     `json:"authors,omitempty"`
	AuthorRefs []AuthorRef `json:"authorRefs,omitempty"`
	CachedOnly bool        `json:"cachedOnly"` // true = local library view
	Keyword    string      `json:"keyword"`
	Sort       string      `json:"sort,omitempty"` // "newest", "oldest"
	Limit      int         `json:"limit"`
	Offset     int         `json:"offset"`
}

// NormalizeAuthorUID resolves canonical (site, author_uid) from site, uid, and legacy mid.
func NormalizeAuthorUID(site, uid string, mid int64) (normSite, normUID string) {
	normSite = strings.ToLower(strings.TrimSpace(site))
	if normSite == "" {
		normSite = "bilibili"
	}
	normUID = strings.TrimSpace(uid)
	if normSite == "bilibili" {
		if normUID == "" && mid > 0 {
			normUID = strconv.FormatInt(mid, 10)
		} else if n, err := strconv.ParseInt(normUID, 10, 64); err == nil && n > 0 {
			normUID = strconv.FormatInt(n, 10)
		}
	}
	return normSite, normUID
}

// AuthorHomeSeenKey returns the multi-site ui-state.json HomeSeen key for an unsubscribed creator.
func AuthorHomeSeenKey(site, uid string) string {
	normSite, normUID := NormalizeAuthorUID(site, uid, 0)
	if normUID == "" {
		return ""
	}
	return "author:" + normSite + ":" + normUID
}

// LookupAuthorHomeSeen reads the latest seen timestamp for (site, uid), falling back
// to legacy "mid:<uid>" entries for Bilibili authors.
func LookupAuthorHomeSeen(homeSeen map[string]int64, site, uid string) int64 {
	if len(homeSeen) == 0 {
		return 0
	}
	normSite, normUID := NormalizeAuthorUID(site, uid, 0)
	if normUID == "" {
		return 0
	}
	ts := homeSeen["author:"+normSite+":"+normUID]
	if normSite == "bilibili" {
		if legacy := homeSeen["mid:"+normUID]; legacy > ts {
			ts = legacy
		}
	}
	return ts
}

// ParseItemMetadata parses an item.json payload, tolerating both string and numeric timestamps/IDs from older versions.
func ParseItemMetadata(raw []byte) (ItemMetadata, error) {
	var flex struct {
		ItemMetadata
		RawAuthor struct {
			Site     string `json:"site"`
			UID      any    `json:"uid"`
			UniqueID string `json:"uniqueId"`
			Mid      any    `json:"mid"`
			Name     string `json:"name"`
			Face     string `json:"face"`
		} `json:"author"`
		RawLive *struct {
			LiveRecordingInfo
			RawRoomID    any    `json:"roomId"`
			RawSnakeRoom any    `json:"room_id"`
			RawWebRID    string `json:"webRid"`
			RawSnakeWeb  string `json:"web_rid"`
		} `json:"live"`
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
	meta.Site = strings.ToLower(strings.TrimSpace(meta.Site))
	if meta.Site == "" {
		meta.Site = "bilibili"
	}
	if meta.Site == "douyin" && strings.EqualFold(meta.Kind, "note") {
		meta.Kind = "dynamic"
	}
	meta.Author.Mid = parseFlexibleUnix(flex.RawAuthor.Mid)
	meta.Author.Site = strings.ToLower(strings.TrimSpace(flex.RawAuthor.Site))
	meta.Author.UID = parseFlexibleIDString(flex.RawAuthor.UID)
	meta.Author.UniqueID = strings.TrimSpace(flex.RawAuthor.UniqueID)
	meta.Author.Name = flex.RawAuthor.Name
	meta.Author.Face = flex.RawAuthor.Face
	if flex.RawLive != nil {
		lv := flex.RawLive.LiveRecordingInfo
		roomRaw := flex.RawLive.RawRoomID
		if roomRaw == nil {
			roomRaw = flex.RawLive.RawSnakeRoom
		}
		if s := parseFlexibleIDString(roomRaw); s != "" {
			if lv.RoomIDStr == "" {
				lv.RoomIDStr = s
			}
			if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
				lv.RoomID = n
			}
		}
		if lv.WebRID == "" {
			lv.WebRID = strings.TrimSpace(flex.RawLive.RawWebRID)
			if lv.WebRID == "" {
				lv.WebRID = strings.TrimSpace(flex.RawLive.RawSnakeWeb)
			}
		}
		meta.Live = &lv
	}
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
	SanitizeItemMetadata(&meta)
	if meta.Status == "" {
		meta.Status = "complete"
	}
	return meta, nil
}

// SanitizeItemMetadata normalizes author fields and strips all signed/expiring URLs
// so offline manifests never persist signed CDN links or credentials.
func SanitizeItemMetadata(meta *ItemMetadata) {
	if meta == nil {
		return
	}
	meta.Site = strings.ToLower(strings.TrimSpace(meta.Site))
	if meta.Site == "" {
		meta.Site = "bilibili"
	}
	if meta.Site == "douyin" && strings.EqualFold(meta.Kind, "note") {
		meta.Kind = "dynamic"
	}
	authorSite := meta.Author.Site
	if authorSite == "" {
		authorSite = meta.Site
	}
	normSite, normUID := NormalizeAuthorUID(authorSite, meta.Author.UID, meta.Author.Mid)
	if meta.Site == "douyin" {
		meta.Author.Site = "douyin"
	} else if meta.Author.UID != "" || meta.Author.Site != "" {
		meta.Author.Site = normSite
	}
	if normSite == "bilibili" {
		if meta.Author.Mid <= 0 && normUID != "" {
			if n, err := strconv.ParseInt(normUID, 10, 64); err == nil && n > 0 {
				meta.Author.Mid = n
			}
		}
		if meta.Author.UID != "" {
			meta.Author.UID = normUID
		}
	} else {
		meta.Author.UID = normUID
	}

	meta.URL = sanitizeManifestPageURL(meta.URL, meta.Site, meta.Kind, meta.RawID, meta.Live)
	if meta.Site == "douyin" {
		// Douyin image/media CDN URLs carry expiring signatures and are never persisted.
		if isRemoteOrSignedURL(meta.Author.Face) {
			meta.Author.Face = ""
		}
		if meta.Music != nil && isRemoteOrSignedURL(meta.Music.Cover) {
			meta.Music.Cover = ""
		}
		if meta.Music != nil && isRemoteOrSignedURL(meta.Music.File) {
			meta.Music.File = ""
		}
	} else {
		meta.Author.Face = stripURLQueryAndFragment(meta.Author.Face)
	}
	for i := range meta.Media {
		if meta.Site == "douyin" {
			if isRemoteOrSignedURL(meta.Media[i].Source) {
				meta.Media[i].Source = ""
			}
		} else if hasSignedOrQueryMarkers(meta.Media[i].Source) {
			meta.Media[i].Source = ""
		}
	}
}

func hasSignedOrQueryMarkers(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range []string{"?", "#", "a_bogus=", "x-bogus=", "x-signature=", "x-expires=", "upsig=", "w_rid=", "sessionid=", "sessdata="} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isRemoteOrSignedURL(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "//") {
		return true
	}
	for _, marker := range []string{"?", "a_bogus=", "x-bogus=", "x-signature=", "x-expires=", "upsig=", "w_rid=", "sessionid=", "sessdata="} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func stripURLQueryAndFragment(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if u, err := url.Parse(trimmed); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	if isRemoteOrSignedURL(trimmed) {
		return ""
	}
	return trimmed
}

func sanitizeManifestPageURL(rawURL, site, kind, rawID string, live *LiveRecordingInfo) string {
	clean := stripURLQueryAndFragment(rawURL)
	if clean != "" {
		return clean
	}
	if site == "douyin" && rawID != "" {
		switch kind {
		case "video":
			return "https://www.douyin.com/video/" + rawID
		case "dynamic":
			return "https://www.douyin.com/note/" + rawID
		case "live":
			if live != nil && strings.TrimSpace(live.WebRID) != "" {
				return "https://live.douyin.com/" + strings.TrimSpace(live.WebRID)
			}
			return "https://live.douyin.com/" + rawID
		}
	}
	return clean
}

func parseFlexibleIDString(v any) string {
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case float64:
		if val > 0 {
			return strconv.FormatInt(int64(val), 10)
		}
	case int64:
		if val > 0 {
			return strconv.FormatInt(val, 10)
		}
	}
	return ""
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
