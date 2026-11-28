package library

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// CurrentSchemaVersion is the SQLite PRAGMA user_version written to library.db.
const CurrentSchemaVersion = 2

// CurrentCacheStandardVersion identifies the current cache completeness and
// media/metadata enrichment baseline recorded on newly cached or re-verified
// item.json manifests (item.json schema version remains 1).
const CurrentCacheStandardVersion = 2

// WeiboCacheStandardVersion identifies the Weibo-specific cache standard
// baseline including dual-save watermark-free (/oslarge/) companion images
// and DEC-073 clean structured links (DEC-078, DEC-079). CurrentCacheStandardVersion
// stays at 2 so Bilibili Gate B does not trigger unnecessary PlayURL re-probes.
const WeiboCacheStandardVersion = 3

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
	// Via is set when the item is stored under another creator's folder
	// than its author's (a forwarded post's content, or a collaboration video
	// cached for a member who is not the uploader; DEC-056). Optional; older
	// manifests have none.
	Via *ItemVia `json:"via,omitempty"`
	// Origin records the retweeted/forwarded original post metadata directly
	// inside item.json (optional; older manifests have none, schema version unchanged).
	Origin *ItemOrigin `json:"origin,omitempty"`
	// InReplyTo records the parent post this item replies to (DEC-091).
	// Optional; older manifests have none and item.json stays version 1.
	InReplyTo *ItemReplyTarget `json:"inReplyTo,omitempty"`
	// Card records a degraded non-media rich card attached to the post
	// (e.g. Weibo article, vote, music, webpage, or live card; no signed URLs).
	Card *ItemRichCard `json:"card,omitempty"`
	// Article records structured long-form article content and local relative
	// media paths (e.g. X Articles, DEC-093). Optional; item.json stays version 1.
	Article *ItemArticle `json:"article,omitempty"`
	// Thread records author self-reply thread segments cached together into the
	// same item (e.g. X author self-reply threads). Optional; older manifests have none.
	Thread []ItemThreadPart `json:"thread,omitempty"`
	// Links records human-readable hyperlink labels and safe HTTP(S) destination
	// URLs extracted from the post body (DEC-073). Optional; older manifests have none.
	Links []ItemLink `json:"links,omitempty"`
	// CachedBy is the TRACE installation that cached the item (DEC-062 ⑧);
	// optional, older manifests have none.
	CachedBy *ItemNode `json:"cachedBy,omitempty"`
	// CacheStandardVersion records the cache capability baseline under which
	// this item was cached or verified; 0 means an older manifest.
	CacheStandardVersion int `json:"cacheStandardVersion,omitempty"`
	// RecheckedAt is the RFC3339 timestamp of the last full-cache / deep
	// update inspection for this item.
	RecheckedAt string `json:"recheckedAt,omitempty"`
	// EditTs and EditCount record sanitized source edit metadata for detecting
	// substantive post/work revisions across full-cache sweeps.
	EditTs    int64 `json:"editTs,omitempty"`
	EditCount int   `json:"editCount,omitempty"`
	// EditHistory stores sanitized historical revisions of an edited post
	// (e.g. Weibo /ajax/statuses/editHistory, DEC-094). Optional; item.json stays version 1.
	EditHistory []ItemEditRevision `json:"editHistory,omitempty"`
	// AssetIDs stores sanitized platform media identifiers (never URLs, query
	// strings, paths, or credentials) in order for V1-vs-V2 diffing.
	AssetIDs []string `json:"assetIds,omitempty"`
	// ArchivedOf is the canonical item key ("<site>:<kind>:<raw_id>") of the
	// live item when this entry is an independent archived V1 snapshot created
	// by "保留两版" (keep_both).
	ArchivedOf string `json:"archivedOf,omitempty"`

	// Runtime-populated fields
	ItemKey    string `json:"itemKey,omitempty"`
	RawID      string `json:"rawId,omitempty"`
	ItemDir    string `json:"itemDir,omitempty"`
	TotalBytes int64  `json:"totalBytes,omitempty"`
}

const (
	LiveIncidentReconnect     = "reconnect"
	LiveIncidentLostSegments  = "lost_segments"
	LiveIncidentSlow          = "slow"
	LiveIncidentDisk          = "disk"
	LiveIncidentConvertFailed = "convert_failed"

	MaxLiveIncidentsPerSession = 200
)

// LiveIncident records an anomaly observed during a live recording session (DEC-051 ③).
// It never stores stream URLs, query parameters, or credentials.
type LiveIncident struct {
	Kind         string  `json:"kind"`                   // "reconnect", "lost_segments", "slow", "disk", "convert_failed"
	At           string  `json:"at"`                     // RFC3339 timestamp when the incident occurred or started
	EndedAt      string  `json:"endedAt,omitempty"`      // RFC3339 timestamp of closure or latest coalesced event
	DurationSec  float64 `json:"durationSec,omitempty"`  // Affected duration in seconds (reconnect wait, lost gap, or slow span)
	LostSegments int     `json:"lostSegments,omitempty"` // Dropped HLS media segment count (>0 for HLS sequence jumps)
	Count        int     `json:"count,omitempty"`        // Number of coalesced occurrences (>=1)
	Part         string  `json:"part,omitempty"`         // Local part file basename (never a URL or directory path)
	Reason       string  `json:"reason,omitempty"`       // Sanitized human-readable reason
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
	LastSequenceAt  string            `json:"lastSequenceAt,omitempty"`
	PendingFile     string            `json:"pendingFile,omitempty"`
	PendingBytes    int64             `json:"pendingBytes,omitempty"`
	PendingDuration float64           `json:"pendingDuration,omitempty"`
	Codec           string            `json:"codec,omitempty"`
	Quality         int               `json:"quality,omitempty"`
	QualityName     string            `json:"qualityName,omitempty"`
	Segments        []LiveSegmentInfo `json:"segments,omitempty"`
	Incidents       []LiveIncident    `json:"incidents,omitempty"`
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
	ItemCount   int    `json:"itemCount,omitempty"`
	TotalBytes  int64  `json:"totalBytes,omitempty"`
}

// Reasons recorded in ItemVia.
const (
	ViaForward = "forward"
	ViaCollab  = "collab"
)

// ItemNode names a TRACE installation: its random node ID and a readable
// name (the computer's or the remote's name).
type ItemNode struct {
	Node string `json:"node"`
	Name string `json:"name,omitempty"`
}

// ItemVia records the creator an item was cached for when that creator is not
// the item's author (DEC-056). It never changes Author.
type ItemVia struct {
	Reason    string `json:"reason"` // ViaForward or ViaCollab
	Site      string `json:"site,omitempty"`
	UID       string `json:"uid"`
	Name      string `json:"name,omitempty"`
	DynamicID string `json:"dynamicId,omitempty"` // forwarding post, for ViaForward
}

// ItemRichCard records sanitized metadata for a degraded non-media card attached to a post.
type ItemRichCard struct {
	Kind  string `json:"kind,omitempty"` // "article", "vote", "music", "webpage", "live"
	Label string `json:"label,omitempty"`
	Title string `json:"title,omitempty"`
	Desc  string `json:"desc,omitempty"`
	URL   string `json:"url,omitempty"`
}

// ItemArticleBlock records one ordered rich-text, inline-image, or embedded-tweet
// block of a cached long-form article (e.g. X Articles, DEC-093).
// It never stores remote CDN URLs, query parameters, or absolute local paths.
type ItemArticleBlock struct {
	Type     string     `json:"type"`
	Text     string     `json:"text,omitempty"`
	Links    []ItemLink `json:"links,omitempty"`
	MediaRel string     `json:"mediaRel,omitempty"`
	TweetID  string     `json:"tweetId,omitempty"`
}

// ItemArticle stores structured long-form article content persisted inside
// item.json (schema version remains 1, DEC-093). CoverRel and Block.MediaRel
// hold only local item-relative paths registered in ItemMetadata.Media.
type ItemArticle struct {
	Title     string             `json:"title,omitempty"`
	CoverRel  string             `json:"coverRel,omitempty"`
	PlainText string             `json:"plainText,omitempty"`
	Blocks    []ItemArticleBlock `json:"blocks,omitempty"`
}

// ItemLink records one human-readable hyperlink label paired with its safe
// external HTTP(S) target URL inside item.json (DEC-073).
type ItemLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// ItemEditRevision records one cached historical revision of an edited post
// persisted inside item.json (schema version remains 1, DEC-094). MediaPaths
// stores only local item-relative paths registered in ItemMetadata.Media (never
// remote CDN URLs, query parameters, signatures, or credentials).
type ItemEditRevision struct {
	Version    int        `json:"version"`
	EditTs     int64      `json:"editTs,omitempty"`
	Text       string     `json:"text,omitempty"`
	Links      []ItemLink `json:"links,omitempty"`
	MediaPaths []string   `json:"mediaPaths,omitempty"`
}

// ItemReplyTarget records sanitized identifiers of the parent post being replied to (DEC-091).
// It never stores URLs, query parameters, signatures, or credentials.
type ItemReplyTarget struct {
	StatusID   string `json:"statusId,omitempty"`
	UserID     string `json:"userId,omitempty"`
	ScreenName string `json:"screenName,omitempty"`
}

// ItemThreadPart records one author self-reply segment persisted inside the same
// cached item (e.g. X author self-reply threads). Optional; library.db stays v2.
type ItemThreadPart struct {
	ID           string      `json:"id,omitempty"`
	PubTs        int64       `json:"pubTs,omitempty"`
	Text         string      `json:"text,omitempty"`
	Links        []ItemLink  `json:"links,omitempty"`
	MediaIndexes []int       `json:"mediaIndexes,omitempty"`
	Origin       *ItemOrigin `json:"origin,omitempty"`
}

// ItemOrigin records the retweeted/forwarded original post snapshot inside item.json.
// All origin media files live inside the forwarder's forwards/ item folder and are tagged
// via NotePageRecord.FromOrigin / MediaFile.FromOrigin / ItemOrigin.VideoFromOrigin.
// Origin allows at most 1 nested level (strictly capped by SanitizeItemMetadata / MergeItemMetadata).
type ItemOrigin struct {
	Site              string           `json:"site,omitempty"`
	ID                string           `json:"id,omitempty"`
	MblogID           string           `json:"mblogid,omitempty"`
	CanonicalURL      string           `json:"canonicalUrl,omitempty"`
	Author            ItemAuthor       `json:"author,omitempty"`
	PubTs             int64            `json:"pubTs,omitempty"`
	Title             string           `json:"title,omitempty"`
	Text              string           `json:"text,omitempty"`
	Links             []ItemLink       `json:"links,omitempty"`
	Card              *ItemRichCard    `json:"card,omitempty"`
	InReplyTo         *ItemReplyTarget `json:"inReplyTo,omitempty"`
	Origin            *ItemOrigin      `json:"origin,omitempty"`
	Unavailable       bool             `json:"unavailable,omitempty"`
	UnavailableReason string           `json:"unavailableReason,omitempty"`
	VideoFromOrigin   bool             `json:"videoFromOrigin,omitempty"`
}

// IsForwardContent reports whether the item is a forwarded post or content
// cached because of one (media library "转发" filter, DEC-056).
func (m ItemMetadata) IsForwardContent() bool {
	if m.Via != nil && m.Via.Reason == ViaForward {
		return true
	}
	dt := strings.TrimSpace(m.DynamicType)
	return m.Kind == "dynamic" && (strings.EqualFold(dt, "DYNAMIC_TYPE_FORWARD") || strings.EqualFold(dt, "forward"))
}

// IsArchivedSnapshot reports whether the item is an independent archived prior
// version ("保留两版") pointing to its original canonical item key.
func (m ItemMetadata) IsArchivedSnapshot() bool {
	return strings.TrimSpace(m.ArchivedOf) != ""
}

// DouyinVideoInfo records Douyin single-file video gear/stream metadata and available gears.
type DouyinVideoInfo struct {
	MediaID        string   `json:"mediaId,omitempty"`
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

// NotePageRecord records one ordered page of a Douyin/Weibo/X image or mixed note (image, motion live-photo, video/GIF, or unknown).
type NotePageRecord struct {
	Index       int              `json:"index"`
	Kind        string           `json:"kind"` // "image", "motion", "video", "unknown"
	MediaID     string           `json:"mediaId,omitempty"`
	GIF         bool             `json:"gif,omitempty"`
	FromOrigin  bool             `json:"fromOrigin,omitempty"`
	UnknownKind string           `json:"unknownKind,omitempty"`
	Image       string           `json:"image,omitempty"`
	NoWatermark string           `json:"noWatermark,omitempty"`
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
	Role       string `json:"role"` // "cover", "video", "audio", "danmaku", "subtitle", "image", "image_nowm", "motion", "live_segment"
	Path       string `json:"path"` // relative to item directory
	GIF        bool   `json:"gif,omitempty"`
	FromOrigin bool   `json:"fromOrigin,omitempty"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256,omitempty"`
	Source     string `json:"source,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Duration   int    `json:"duration,omitempty"`
	// Motion is the motion clip of a live photo (实况), relative to the item
	// directory; set on the still image.
	Motion string `json:"motion,omitempty"`
}

// VideoChapter records one chapter marker (看点) for a video part inside item.json.
// Optional; older item.json manifests omit it with full backward compatibility.
type VideoChapter struct {
	FromSec int    `json:"fromSec"`
	ToSec   int    `json:"toSec"`
	Title   string `json:"title"`
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
	Chapters       []VideoChapter     `json:"chapters,omitempty"`
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

// DeletePlan describes the exact files and items that PlanDelete / PlanDeleteBatch / Delete / DeleteBatch will remove or preserve (FR-LIB-003, INV-09).
type DeletePlan struct {
	TargetKey             string   `json:"targetKey"`
	TargetKeys            []string `json:"targetKeys,omitempty"`
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
	Site string `json:"site,omitempty"` // "all"/"", "bilibili", "douyin", "weibo", "x"
	Kind string `json:"kind"`           // "all", "video", "dynamic", "live", "image", "forward"
	// ExcludeForward drops forwarded content from kind-specific lists (the
	// media library's 图片/视频 tabs; "转发" lists it separately, DEC-056).
	ExcludeForward bool        `json:"excludeForward,omitempty"`
	Mid            int64       `json:"mid"` // 0 means all creators
	AuthorUID      string      `json:"authorUid,omitempty"`
	Authors        []int64     `json:"authors,omitempty"`
	AuthorRefs     []AuthorRef `json:"authorRefs,omitempty"`
	ItemKeys       []string    `json:"itemKeys,omitempty"`
	CachedOnly     bool        `json:"cachedOnly"` // true = local library view
	Keyword        string      `json:"keyword"`
	Sort           string      `json:"sort,omitempty"` // "newest", "oldest"
	Limit          int         `json:"limit"`
	Offset         int         `json:"offset"`
}

// NormalizeAuthorUID resolves canonical (site, author_uid) from site, uid, and legacy mid.
func NormalizeAuthorUID(site, uid string, mid int64) (normSite, normUID string) {
	normSite = strings.ToLower(strings.TrimSpace(site))
	if normSite == "" {
		normSite = "bilibili"
	}
	normUID = strings.TrimSpace(uid)
	switch normSite {
	case "bilibili":
		if normUID == "" && mid > 0 {
			normUID = strconv.FormatInt(mid, 10)
		} else if n, err := strconv.ParseInt(normUID, 10, 64); err == nil && n > 0 {
			normUID = strconv.FormatInt(n, 10)
		}
	case "weibo":
		if normUID != "" {
			allDigits := true
			for _, r := range normUID {
				if r < '0' || r > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				if trimmed := strings.TrimLeft(normUID, "0"); trimmed != "" {
					normUID = trimmed
				}
			}
		}
	case "x":
		if normUID != "" {
			allDigits := true
			for _, r := range normUID {
				if r < '0' || r > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				if trimmed := strings.TrimLeft(normUID, "0"); trimmed != "" {
					if _, err := strconv.ParseUint(trimmed, 10, 64); err == nil {
						normUID = trimmed
					}
				}
			}
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
		RawRecheckedAt      any   `json:"recheckedAt"`
		RawEditTs           any   `json:"editTs"`
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
	meta.RecheckedAt = parseFlexibleTimestamp(flex.RawRecheckedAt)
	meta.EditTs = parseFlexibleUnix(flex.RawEditTs)
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
	if meta.Site == "weibo" && meta.Kind == "video" {
		meta.RawID = strings.ReplaceAll(meta.RawID, ":", "_")
	}
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
	if meta.Site == "douyin" || meta.Site == "weibo" || meta.Site == "x" {
		meta.Author.Site = meta.Site
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
	switch meta.Site {
	case "douyin":
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
	case "weibo":
		meta.Author.Face = sanitizeWeiboManifestURL(meta.Author.Face)
	case "x":
		meta.Author.Face = sanitizeXManifestURL(meta.Author.Face)
	default:
		meta.Author.Face = stripURLQueryAndFragment(meta.Author.Face)
	}
	for i := range meta.Media {
		switch meta.Site {
		case "douyin", "weibo", "x":
			if isRemoteOrSignedURL(meta.Media[i].Source) || hasSignedOrQueryMarkers(meta.Media[i].Source) {
				meta.Media[i].Source = ""
			}
		default:
			if hasSignedOrQueryMarkers(meta.Media[i].Source) {
				meta.Media[i].Source = ""
			}
		}
	}
	if meta.Live != nil {
		meta.Live.LastSequenceAt = strings.TrimSpace(meta.Live.LastSequenceAt)
		if hasSignedOrQueryMarkers(meta.Live.LastSequenceAt) || isRemoteOrSignedURL(meta.Live.LastSequenceAt) {
			meta.Live.LastSequenceAt = ""
		}
		if len(meta.Live.Incidents) > 0 {
			meta.Live.Incidents = SanitizeLiveIncidents(meta.Live.Incidents)
		}
	}
	if meta.Site == "x" {
		if containsXForbiddenTextMarkers(meta.Title) {
			meta.Title = scrubManifestFreeText(meta.Title)
		}
		if containsXForbiddenTextMarkers(meta.Desc) {
			meta.Desc = scrubManifestFreeText(meta.Desc)
		}
		if containsXForbiddenTextMarkers(meta.Text) {
			meta.Text = scrubManifestFreeText(meta.Text)
		}
	}
	meta.Links = sanitizeItemLinks(meta.Links)
	meta.InReplyTo = sanitizeItemReplyTarget(meta.InReplyTo, meta.Site)
	if len(meta.Thread) > 0 {
		cleaned := make([]ItemThreadPart, 0, len(meta.Thread))
		for _, part := range meta.Thread {
			part.ID = strings.TrimSpace(part.ID)
			part.Text = scrubManifestFreeText(part.Text)
			part.Links = sanitizeItemLinks(part.Links)
			if part.Origin != nil {
				sanitizeItemOriginDepth(part.Origin, meta.Site, 0)
				if isEmptyItemOrigin(part.Origin) {
					part.Origin = nil
				}
			}
			if part.ID == "" && part.Text == "" && len(part.Links) == 0 && len(part.MediaIndexes) == 0 && part.Origin == nil {
				continue
			}
			cleaned = append(cleaned, part)
		}
		meta.Thread = cleaned
	}
	if meta.Card != nil {
		sanitizeItemRichCard(meta.Card)
		if meta.Card.Kind == "" && meta.Card.Label == "" && meta.Card.Title == "" && meta.Card.Desc == "" && meta.Card.URL == "" {
			meta.Card = nil
		}
	}
	meta.Article = sanitizeItemArticle(meta.Article, meta.Site)
	meta.EditHistory = sanitizeItemEditHistory(meta.EditHistory)
	if meta.Origin != nil {
		sanitizeItemOriginDepth(meta.Origin, meta.Site, 0)
	}
	for i := range meta.Parts {
		if len(meta.Parts[i].Chapters) > 0 {
			cleaned := make([]VideoChapter, 0, len(meta.Parts[i].Chapters))
			for _, ch := range meta.Parts[i].Chapters {
				ch.Title = strings.TrimSpace(ch.Title)
				if ch.Title == "" || ch.FromSec < 0 || ch.ToSec <= ch.FromSec {
					continue
				}
				cleaned = append(cleaned, ch)
			}
			if len(cleaned) == 0 {
				meta.Parts[i].Chapters = nil
			} else {
				for j := 1; j < len(cleaned); j++ {
					cur := cleaned[j]
					k := j - 1
					for k >= 0 && (cleaned[k].FromSec > cur.FromSec || (cleaned[k].FromSec == cur.FromSec && cleaned[k].ToSec > cur.ToSec)) {
						cleaned[k+1] = cleaned[k]
						k--
					}
					cleaned[k+1] = cur
				}
				meta.Parts[i].Chapters = cleaned
			}
		}
	}
	sanitizeManifestConflictFields(meta)
}

func sanitizeManifestConflictFields(meta *ItemMetadata) {
	if meta == nil {
		return
	}
	if meta.CacheStandardVersion < 0 {
		meta.CacheStandardVersion = 0
	}
	meta.RecheckedAt = strings.TrimSpace(meta.RecheckedAt)
	if hasSignedOrQueryMarkers(meta.RecheckedAt) || isRemoteOrSignedURL(meta.RecheckedAt) || containsXForbiddenTextMarkers(meta.RecheckedAt) {
		meta.RecheckedAt = ""
	}
	if meta.EditTs < 0 {
		meta.EditTs = 0
	}
	if meta.EditCount < 0 {
		meta.EditCount = 0
	}
	if len(meta.AssetIDs) > 0 {
		cleaned := make([]string, 0, len(meta.AssetIDs))
		for _, raw := range meta.AssetIDs {
			if tok := SanitizeMediaToken(raw); tok != "" {
				cleaned = append(cleaned, tok)
			}
		}
		if len(cleaned) == 0 {
			meta.AssetIDs = nil
		} else {
			meta.AssetIDs = cleaned
		}
	} else {
		meta.AssetIDs = nil
	}
	if canon, ok := ValidateCanonicalItemKey(meta.ArchivedOf); ok {
		selfKey := strings.TrimSpace(meta.ItemKey)
		if selfKey == "" && strings.TrimSpace(meta.Kind) != "" && strings.TrimSpace(meta.ID) != "" {
			selfKey = CanonicalItemKey(meta.Site, meta.Kind, meta.ID)
		}
		if selfKey != "" && strings.EqualFold(canon, selfKey) {
			meta.ArchivedOf = ""
		} else {
			meta.ArchivedOf = canon
		}
	} else {
		meta.ArchivedOf = ""
	}
	if meta.DouyinVideo != nil {
		meta.DouyinVideo.MediaID = SanitizeMediaToken(meta.DouyinVideo.MediaID)
	}
	for i := range meta.NotePages {
		meta.NotePages[i].MediaID = SanitizeMediaToken(meta.NotePages[i].MediaID)
		if meta.NotePages[i].VideoInfo != nil {
			meta.NotePages[i].VideoInfo.MediaID = SanitizeMediaToken(meta.NotePages[i].VideoInfo.MediaID)
		}
	}
}

// SanitizeMediaToken validates a platform media/asset identifier (e.g. Douyin
// vid/uri token, Weibo pic_id or object_id, X media_key, Bilibili cid/bvid) and
// returns "" if it contains any URL, domain, query string, path separator,
// traversal segment, whitespace, control character, or credential marker.
func SanitizeMediaToken(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 160 {
		return ""
	}
	if isRemoteOrSignedURL(trimmed) || hasSignedOrQueryMarkers(trimmed) || containsXForbiddenTextMarkers(trimmed) {
		return ""
	}
	if strings.Contains(trimmed, "..") || strings.Contains(trimmed, "://") || strings.Contains(trimmed, "::") {
		return ""
	}
	first, last := trimmed[0], trimmed[len(trimmed)-1]
	if first == '.' || first == ':' || last == '.' || last == ':' {
		return ""
	}
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '_' || c == '-' || c == ':' || c == '.'
		if !ok {
			return ""
		}
	}
	lower := strings.ToLower(trimmed)
	for _, forbidden := range []string{
		".com", ".cn", ".tv", ".net", ".org", ".io",
		"weibocdn", "sinaimg", "twimg", "douyinvod", "byteimg", "hdslb", "bilivideo", "pscp", "periscope",
		"cookie", "bearer", "sessdata", "bili_jct", "sessionid", "auth_token",
	} {
		if strings.Contains(lower, forbidden) {
			return ""
		}
	}
	return trimmed
}

// ValidateCanonicalItemKey validates raw as a canonical "<site>:<kind>:<raw_id>"
// item key for supported sites/kinds and returns the normalized key and true, or
// ("", false) when invalid or containing unsafe characters/markers.
func ValidateCanonicalItemKey(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 200 {
		return "", false
	}
	if isRemoteOrSignedURL(trimmed) || hasSignedOrQueryMarkers(trimmed) || containsXForbiddenTextMarkers(trimmed) {
		return "", false
	}
	parts := strings.SplitN(trimmed, ":", 3)
	if len(parts) != 3 {
		return "", false
	}
	site := strings.ToLower(strings.TrimSpace(parts[0]))
	switch site {
	case "bilibili", "douyin", "weibo", "x":
	default:
		return "", false
	}
	kind := strings.ToLower(strings.TrimSpace(parts[1]))
	if site == "douyin" && kind == "note" {
		kind = "dynamic"
	}
	switch kind {
	case "video", "dynamic", "live":
	default:
		return "", false
	}
	rawID := strings.TrimSpace(parts[2])
	if site == "weibo" && kind == "video" {
		rawID = strings.ReplaceAll(rawID, ":", "_")
	}
	if rawID == "" || NormalizeRawID(kind, rawID) != rawID {
		return "", false
	}
	if err := ValidateSafeIDSegment(rawID); err != nil {
		return "", false
	}
	return site + ":" + kind + ":" + rawID, true
}

const maxItemOriginNestingDepth = 1

// SanitizeItemReplyTarget scrubs reply context identifiers so item.json never
// persists URLs, query markers, signed tokens, or credentials (DEC-091).
func SanitizeItemReplyTarget(target *ItemReplyTarget, site string) *ItemReplyTarget {
	return sanitizeItemReplyTarget(target, site)
}

func sanitizeItemReplyTarget(target *ItemReplyTarget, site string) *ItemReplyTarget {
	if target == nil {
		return nil
	}
	normSite := strings.ToLower(strings.TrimSpace(site))
	statusID := sanitizeReplyIDToken(target.StatusID, normSite)
	userID := sanitizeReplyUserID(target.UserID, normSite)
	screenName := sanitizeReplyScreenName(target.ScreenName)
	if statusID == "" && userID == "" && screenName == "" {
		return nil
	}
	return &ItemReplyTarget{
		StatusID:   statusID,
		UserID:     userID,
		ScreenName: screenName,
	}
}

func sanitizeReplyIDToken(raw, normSite string) string {
	tok := SanitizeMediaToken(raw)
	if tok == "" {
		return ""
	}
	if normSite == "x" {
		for i := 0; i < len(tok); i++ {
			if tok[i] < '0' || tok[i] > '9' {
				return ""
			}
		}
		trimmed := strings.TrimLeft(tok, "0")
		if trimmed == "" {
			return ""
		}
		if _, err := strconv.ParseUint(trimmed, 10, 64); err != nil {
			return ""
		}
		return trimmed
	}
	return tok
}

func sanitizeReplyUserID(raw, normSite string) string {
	tok := SanitizeMediaToken(raw)
	if tok == "" {
		return ""
	}
	if normSite == "x" {
		return sanitizeReplyIDToken(tok, "x")
	}
	if normSite != "" {
		_, normUID := NormalizeAuthorUID(normSite, tok, 0)
		return SanitizeMediaToken(normUID)
	}
	return tok
}

// NormalizeDisplayTypeLabel returns the user-facing type label for a cached
// item, upgrading legacy X quote manifests (persisted before DEC-091 with
// TypeLabel "转发" or empty) to "引用帖子" while keeping pure retweets
// ("RT @..." or empty outer text) distinct from quote posts.
func (m ItemMetadata) NormalizeDisplayTypeLabel() string {
	lbl := strings.TrimSpace(m.TypeLabel)
	if strings.EqualFold(strings.TrimSpace(m.Site), "x") && m.Kind == "dynamic" {
		trimmedText := strings.TrimSpace(m.Text)
		isQuote := m.Origin != nil && trimmedText != "" && !strings.HasPrefix(trimmedText, "RT @")
		if lbl == "转发" && isQuote {
			return "引用帖子"
		}
		if lbl == "" {
			if m.Article != nil {
				return "文章"
			}
			if isQuote {
				return "引用帖子"
			}
			if (m.Via != nil && m.Via.Reason == ViaForward) || m.Origin != nil {
				return "转发帖子"
			}
			return "帖子"
		}
	}
	return lbl
}

func sanitizeReplyScreenName(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "@")
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 {
		return ""
	}
	if isRemoteOrSignedURL(s) || hasSignedOrQueryMarkers(s) || containsXForbiddenTextMarkers(s) {
		return ""
	}
	if strings.Contains(s, "://") || strings.Contains(s, "..") {
		return ""
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ""
		}
		switch r {
		case '/', '\\', '?', '#', '&', '=', ':', ';', '\'', '"', '<', '>':
			return ""
		}
	}
	lower := strings.ToLower(s)
	for _, forbidden := range []string{
		".com", ".cn", ".tv", ".net", ".org", ".io",
		"twimg", "weibocdn", "sinaimg", "douyinvod",
	} {
		if strings.Contains(lower, forbidden) {
			return ""
		}
	}
	return s
}

func isEmptyItemOrigin(orig *ItemOrigin) bool {
	if orig == nil {
		return true
	}
	return orig.ID == "" &&
		orig.MblogID == "" &&
		orig.CanonicalURL == "" &&
		orig.Author.UID == "" &&
		orig.Author.UniqueID == "" &&
		orig.Author.Name == "" &&
		orig.PubTs == 0 &&
		orig.Title == "" &&
		orig.Text == "" &&
		len(orig.Links) == 0 &&
		orig.Card == nil &&
		orig.InReplyTo == nil &&
		orig.Origin == nil &&
		!orig.Unavailable &&
		orig.UnavailableReason == "" &&
		!orig.VideoFromOrigin
}

func sanitizeItemOrigin(orig *ItemOrigin, defaultSite string) {
	sanitizeItemOriginDepth(orig, defaultSite, 0)
}

func sanitizeItemOriginDepth(orig *ItemOrigin, defaultSite string, depth int) {
	if orig == nil {
		return
	}
	orig.Site = strings.ToLower(strings.TrimSpace(orig.Site))
	if orig.Site == "" {
		orig.Site = defaultSite
	}
	orig.ID = strings.TrimSpace(orig.ID)
	orig.MblogID = strings.TrimSpace(orig.MblogID)
	orig.Title = scrubManifestFreeText(orig.Title)
	orig.Text = scrubManifestFreeText(orig.Text)
	orig.Links = sanitizeItemLinks(orig.Links)
	orig.UnavailableReason = scrubManifestFreeText(orig.UnavailableReason)
	orig.InReplyTo = sanitizeItemReplyTarget(orig.InReplyTo, orig.Site)

	authorSite := orig.Author.Site
	if authorSite == "" {
		authorSite = orig.Site
	}
	normSite, normUID := NormalizeAuthorUID(authorSite, orig.Author.UID, orig.Author.Mid)
	if orig.Site == "douyin" || orig.Site == "weibo" || orig.Site == "x" {
		orig.Author.Site = orig.Site
	} else if orig.Author.UID != "" || orig.Author.Site != "" {
		orig.Author.Site = normSite
	}
	if normSite == "bilibili" {
		if orig.Author.Mid <= 0 && normUID != "" {
			if n, err := strconv.ParseInt(normUID, 10, 64); err == nil && n > 0 {
				orig.Author.Mid = n
			}
		}
		if orig.Author.UID != "" {
			orig.Author.UID = normUID
		}
	} else {
		orig.Author.UID = normUID
	}
	orig.Author.UniqueID = strings.TrimSpace(orig.Author.UniqueID)
	orig.Author.Name = strings.TrimSpace(orig.Author.Name)
	switch orig.Site {
	case "douyin":
		if isRemoteOrSignedURL(orig.Author.Face) {
			orig.Author.Face = ""
		}
	case "weibo":
		orig.Author.Face = sanitizeWeiboManifestURL(orig.Author.Face)
	case "x":
		orig.Author.Face = sanitizeXManifestURL(orig.Author.Face)
	default:
		orig.Author.Face = stripURLQueryAndFragment(orig.Author.Face)
	}

	if orig.CanonicalURL != "" || orig.ID != "" {
		orig.CanonicalURL = sanitizeManifestPageURL(orig.CanonicalURL, orig.Site, "dynamic", orig.ID, nil)
	}
	if orig.Card != nil {
		sanitizeItemRichCard(orig.Card)
		if orig.Card.Kind == "" && orig.Card.Label == "" && orig.Card.Title == "" && orig.Card.Desc == "" && orig.Card.URL == "" {
			orig.Card = nil
		}
	}
	// Strictly cap nested Origin to 1 level (DEC-091) to prevent recursion/infinite nesting.
	if depth >= maxItemOriginNestingDepth {
		orig.Origin = nil
	} else if orig.Origin != nil {
		sanitizeItemOriginDepth(orig.Origin, orig.Site, depth+1)
		if isEmptyItemOrigin(orig.Origin) {
			orig.Origin = nil
		}
	}
}

func sanitizeItemRichCard(card *ItemRichCard) {
	if card == nil {
		return
	}
	card.Kind = strings.TrimSpace(card.Kind)
	card.Label = strings.TrimSpace(card.Label)
	card.Title = scrubManifestFreeText(card.Title)
	card.Desc = scrubManifestFreeText(card.Desc)
	card.URL = sanitizeRichCardURL(card.URL)
}

const maxArticleBlocksPerManifest = 1000

// SanitizeItemArticle scrubs an article structure so item.json never stores
// remote CDN URLs, query parameters, path traversals, or credential markers (DEC-093).
func SanitizeItemArticle(art *ItemArticle, site string) *ItemArticle {
	return sanitizeItemArticle(art, site)
}

func sanitizeItemArticle(art *ItemArticle, site string) *ItemArticle {
	if art == nil {
		return nil
	}
	normSite := strings.ToLower(strings.TrimSpace(site))
	out := &ItemArticle{
		Title:     scrubManifestFreeText(art.Title),
		CoverRel:  sanitizeArticleRelPath(art.CoverRel),
		PlainText: scrubManifestFreeText(art.PlainText),
	}
	if len(art.Blocks) > 0 {
		blocks := make([]ItemArticleBlock, 0, min(len(art.Blocks), maxArticleBlocksPerManifest))
		for _, blk := range art.Blocks {
			cleanBlk := ItemArticleBlock{
				Type:     normalizeItemArticleBlockType(blk.Type),
				Text:     scrubManifestFreeText(blk.Text),
				Links:    sanitizeItemLinks(blk.Links),
				MediaRel: sanitizeArticleRelPath(blk.MediaRel),
				TweetID:  sanitizeReplyIDToken(blk.TweetID, normSite),
			}
			if cleanBlk.Text == "" && len(cleanBlk.Links) == 0 && cleanBlk.MediaRel == "" && cleanBlk.TweetID == "" {
				continue
			}
			blocks = append(blocks, cleanBlk)
			if len(blocks) >= maxArticleBlocksPerManifest {
				break
			}
		}
		if len(blocks) > 0 {
			out.Blocks = blocks
		}
	}
	if out.Title == "" && out.CoverRel == "" && out.PlainText == "" && len(out.Blocks) == 0 {
		return nil
	}
	return out
}

func normalizeItemArticleBlockType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "header-one", "header-two", "blockquote", "ordered-list-item", "unordered-list-item", "atomic":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "unstyled"
	}
}

func sanitizeArticleRelPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 260 {
		return ""
	}
	if isRemoteOrSignedURL(trimmed) || hasSignedOrQueryMarkers(trimmed) || containsXForbiddenTextMarkers(trimmed) {
		return ""
	}
	if strings.Contains(trimmed, "://") || strings.ContainsAny(trimmed, "\\:\x00?#") {
		return ""
	}
	if strings.HasPrefix(trimmed, "/") {
		return ""
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return ""
		}
	}
	parts := strings.Split(trimmed, "/")
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return ""
		}
	}
	clean := cleanManifestRel(trimmed)
	if clean == "" || strings.EqualFold(clean, "item.json") || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || clean == ".." {
		return ""
	}
	return clean
}

const (
	maxEditHistoryRevisionsPerManifest = 100
	maxEditRevisionMediaPaths          = 36
)

// SanitizeItemEditHistory scrubs historical post revisions so item.json never
// persists remote URLs, signed query parameters, path traversals, or credential
// markers (DEC-094).
func SanitizeItemEditHistory(revs []ItemEditRevision) []ItemEditRevision {
	return sanitizeItemEditHistory(revs)
}

func sanitizeItemEditHistory(revs []ItemEditRevision) []ItemEditRevision {
	if len(revs) == 0 {
		return nil
	}
	out := make([]ItemEditRevision, 0, min(len(revs), maxEditHistoryRevisionsPerManifest))
	for _, rev := range revs {
		editTs := rev.EditTs
		if editTs < 0 {
			editTs = 0
		}
		text := scrubManifestFreeText(rev.Text)
		links := sanitizeItemLinks(rev.Links)
		var mediaPaths []string
		if len(rev.MediaPaths) > 0 {
			seenPath := make(map[string]bool, len(rev.MediaPaths))
			for _, rawPath := range rev.MediaPaths {
				clean := sanitizeArticleRelPath(rawPath)
				if clean == "" {
					continue
				}
				key := strings.ToLower(clean)
				if seenPath[key] {
					continue
				}
				seenPath[key] = true
				mediaPaths = append(mediaPaths, clean)
				if len(mediaPaths) >= maxEditRevisionMediaPaths {
					break
				}
			}
		}
		if text == "" && len(links) == 0 && len(mediaPaths) == 0 && editTs == 0 {
			continue
		}
		ver := rev.Version
		if ver < 0 {
			ver = 0
		}
		out = append(out, ItemEditRevision{
			Version:    ver,
			EditTs:     editTs,
			Text:       text,
			Links:      links,
			MediaPaths: mediaPaths,
		})
		if len(out) >= maxEditHistoryRevisionsPerManifest {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	for i := range out {
		if out[i].Version <= 0 {
			v := len(out) - i
			if v < 1 {
				v = 1
			}
			out[i].Version = v
		}
	}
	return out
}

const maxItemLinksPerManifest = 64

// SanitizeItemLinks filters and normalizes external hyperlinks to safe HTTP(S)
// destinations, rejecting non-HTTP(S) schemes, userinfo, control characters,
// signed query markers, and forbidden media CDN hosts.
func SanitizeItemLinks(links []ItemLink) []ItemLink {
	return sanitizeItemLinks(links)
}

func sanitizeItemLinks(links []ItemLink) []ItemLink {
	if len(links) == 0 {
		return nil
	}
	out := make([]ItemLink, 0, min(len(links), maxItemLinksPerManifest))
	for _, link := range links {
		cleanURL := sanitizeItemLinkURL(link.URL)
		if cleanURL == "" {
			continue
		}
		label := scrubManifestFreeText(link.Label)
		label = strings.Join(strings.Fields(label), " ")
		if utf8.RuneCountInString(label) > 120 {
			runes := []rune(label)
			label = string(runes[:120]) + "…"
		}
		if label == "" {
			label = "网页链接"
		}
		out = append(out, ItemLink{
			Label: label,
			URL:   cleanURL,
		})
		if len(out) >= maxItemLinksPerManifest {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizeItemLinkURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	for _, r := range trimmed {
		if r <= 0x1f || r == 0x7f || unicode.IsSpace(r) || r == '\\' {
			return ""
		}
	}
	clean := sanitizeRichCardURL(trimmed)
	if clean == "" {
		return ""
	}
	u, err := url.Parse(clean)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "sinaimg.cn" || strings.HasSuffix(host, ".sinaimg.cn") || host == "pbs.twimg.com" {
		return ""
	}
	return clean
}

func isForbiddenXMediaHost(host string) bool {
	return host == "video.twimg.com" ||
		host == "video-s.twimg.com" ||
		host == "ton.x.com" ||
		host == "ton.twitter.com" ||
		host == "pscp.tv" || strings.HasSuffix(host, ".pscp.tv") ||
		host == "periscope.tv" || strings.HasSuffix(host, ".periscope.tv")
}

func sanitizeRichCardURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || hasSignedTokenMarkers(trimmed) {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "weibocdn.com" || strings.HasSuffix(host, ".weibocdn.com") || host == "video.weibo.com" || isForbiddenXMediaHost(host) {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

var (
	reManifestURLSpan      = regexp.MustCompile(`(?i)https?://[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]+`)
	reManifestSecretToken  = regexp.MustCompile(`(?i)\b(SUB|SUBP|XSRF-TOKEN|X-XSRF-TOKEN|WBPSESS|SESSDATA|BILI_JCT|SESSIONID|SSIG|EXPIRES|KID|SCF|ALF|SSOLOGINSTATE|SUHB|A_BOGUS|X-BOGUS|X-SIGNATURE|W_RID|UPSIG|AUTH_TOKEN|CT0|KDT|TWID|GUEST_ID|X-CSRF-TOKEN|X-CLIENT-TRANSACTION-ID)\s*=\s*[^\s;,&"'()<>]+`)
	reManifestHeaderSecret = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*["']?bearer\s+[^\s;,&"'()<>]+|(?:x-csrf-token|x-client-transaction-id)\s*:\s*[^\s;,&"'()<>]+)`)
)

func containsXForbiddenTextMarkers(s string) bool {
	if hasSignedTokenMarkers(s) {
		return true
	}
	lower := strings.ToLower(s)
	return strings.Contains(lower, "video.twimg.com") ||
		strings.Contains(lower, "video-s.twimg.com") ||
		strings.Contains(lower, "ton.x.com") ||
		strings.Contains(lower, "ton.twitter.com") ||
		strings.Contains(lower, "pscp.tv") ||
		strings.Contains(lower, "periscope.tv")
}

// ScrubComparableManifestText applies the exact free-text URL/token scrubbing
// used when persisting item.json so live remote post text and stored manifest
// text compare identically without false conflicts on scrubbed media/signed URLs.
func ScrubComparableManifestText(raw string) string {
	return scrubManifestFreeText(raw)
}

func scrubManifestFreeText(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return strings.TrimSpace(raw)
	}
	out := reManifestURLSpan.ReplaceAllStringFunc(raw, func(tok string) string {
		trimmed := strings.TrimRight(tok, ".,;:!?)]}\"'")
		suffix := tok[len(trimmed):]
		if u, err := url.Parse(trimmed); err == nil {
			host := strings.ToLower(u.Hostname())
			if host == "weibocdn.com" || strings.HasSuffix(host, ".weibocdn.com") ||
				(host == "video.weibo.com" && strings.EqualFold(u.Path, "/media/play")) ||
				isForbiddenXMediaHost(host) {
				return suffix
			}
		}
		if !hasSignedTokenMarkers(tok) {
			return tok
		}
		clean := stripURLQueryAndFragment(trimmed)
		if clean == "" {
			return suffix
		}
		return clean + suffix
	})
	if hasSignedTokenMarkers(out) {
		out = reManifestHeaderSecret.ReplaceAllString(out, "")
		out = reManifestSecretToken.ReplaceAllString(out, "")
	}
	return strings.TrimSpace(out)
}

func hasSignedTokenMarkers(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range []string{
		"a_bogus=", "x-bogus=", "x-signature=", "x-expires=",
		"upsig=", "w_rid=", "sessionid=", "sessdata=", "bili_jct=", "ac_time_value=",
		"ssig=", "expires=", "kid=",
		"ssig%3d", "expires%3d", "kid%3d",
		"sub=", "subp=", "xsrf-token=", "x-xsrf-token=", "wbpsess=",
		"sub%3d", "subp%3d", "xsrf-token%3d", "wbpsess%3d",
		"scf=", "alf=", "alc=", "ssologinstate=", "suhb=", "pc_token=", "_t_wm=",
		"auth_token=", "auth_token%3d",
		"ct0=", "ct0%3d",
		"kdt=", "kdt%3d",
		"twid=", "twid%3d",
		"guest_id=", "guest_id%3d",
		"x-csrf-token=", "x-csrf-token%3d", "x-csrf-token:", "x-csrf-token%3a",
		"x-client-transaction-id=", "x-client-transaction-id%3d", "x-client-transaction-id:", "x-client-transaction-id%3a",
		"authorization: bearer", "authorization:bearer", "authorization%3a",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return reManifestHeaderSecret.MatchString(trimmed)
}

// SanitizeLiveIncidents strips any accidental URLs, query parameters, paths, or cookies
// from live recording incidents and enforces MaxLiveIncidentsPerSession.
func SanitizeLiveIncidents(incidents []LiveIncident) []LiveIncident {
	if len(incidents) == 0 {
		return nil
	}
	out := make([]LiveIncident, 0, min(len(incidents), MaxLiveIncidentsPerSession))
	for _, inc := range incidents {
		inc.Kind = strings.TrimSpace(inc.Kind)
		inc.At = strings.TrimSpace(inc.At)
		inc.EndedAt = strings.TrimSpace(inc.EndedAt)
		if inc.Kind == "" {
			continue
		}
		if hasSignedOrQueryMarkers(inc.At) || isRemoteOrSignedURL(inc.At) {
			inc.At = ""
		}
		if hasSignedOrQueryMarkers(inc.EndedAt) || isRemoteOrSignedURL(inc.EndedAt) {
			inc.EndedAt = ""
		}
		if inc.DurationSec < 0 {
			inc.DurationSec = 0
		}
		if inc.LostSegments < 0 {
			inc.LostSegments = 0
		}
		if inc.Count < 1 {
			inc.Count = 1
		}
		inc.Part = sanitizeLiveIncidentPart(inc.Part)
		inc.Reason = SanitizeLiveIncidentText(inc.Reason)
		out = append(out, inc)
		if len(out) >= MaxLiveIncidentsPerSession {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizeLiveIncidentPart(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || isRemoteOrSignedURL(trimmed) {
		return ""
	}
	if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	if idx := strings.LastIndexAny(trimmed, `/\`); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" || trimmed == "." || trimmed == ".." || hasSignedOrQueryMarkers(trimmed) {
		return ""
	}
	return trimmed
}

// SanitizeLiveIncidentText removes any URLs, query strings, or credential markers from incident text.
func SanitizeLiveIncidentText(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	fields := strings.Fields(trimmed)
	kept := make([]string, 0, len(fields))
	for _, tok := range fields {
		clean := strings.Trim(tok, "()[]{}<>\"',;")
		lower := strings.ToLower(clean)
		if strings.HasPrefix(lower, "http://") ||
			strings.HasPrefix(lower, "https://") ||
			strings.HasPrefix(lower, "//") ||
			strings.Contains(lower, "://") ||
			hasSignedOrQueryMarkers(clean) ||
			strings.Contains(lower, "cookie") ||
			strings.Contains(lower, "bili_jct") ||
			strings.Contains(lower, "ac_time_value") ||
			strings.Contains(lower, "ttwid") ||
			strings.Contains(lower, "odin_tt") ||
			strings.Contains(lower, "auth_token") ||
			strings.Contains(lower, "x-csrf-token") {
			continue
		}
		if idx := strings.IndexAny(tok, "?#"); idx >= 0 {
			tok = tok[:idx]
		}
		if tok != "" {
			kept = append(kept, tok)
		}
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}

func hasSignedOrQueryMarkers(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, "?#") {
		return true
	}
	return hasSignedTokenMarkers(trimmed)
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
	return hasSignedOrQueryMarkers(trimmed)
}

func sanitizeWeiboManifestURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, signedMarker := range []string{
		"ssig=", "expires=", "kid=",
		"ssig%3d", "expires%3d", "kid%3d",
		"sub=", "subp=", "xsrf-token=", "wbpsess=",
		"sub%3d", "subp%3d", "xsrf-token%3d", "wbpsess%3d",
	} {
		if strings.Contains(lower, signedMarker) {
			return ""
		}
	}
	clean := stripURLQueryAndFragment(trimmed)
	if clean == "" || hasSignedOrQueryMarkers(clean) {
		return ""
	}
	if u, err := url.Parse(clean); err == nil {
		host := strings.ToLower(u.Hostname())
		if host == "weibocdn.com" || strings.HasSuffix(host, ".weibocdn.com") || host == "video.weibo.com" {
			return ""
		}
	}
	return clean
}

// sanitizeXManifestURL allows only clean public pbs.twimg.com image/avatar URLs
// (optionally with public format/name parameters) and strips all video, private,
// or signed URLs per x.md §15.1 #12.
func sanitizeXManifestURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || hasSignedTokenMarkers(trimmed) {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil {
		return ""
	}
	if port := u.Port(); port != "" && port != "443" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "pbs.twimg.com" {
		return ""
	}
	if u.RawQuery != "" {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return ""
		}
		cleanQ := url.Values{}
		for k, vals := range q {
			if len(vals) != 1 {
				return ""
			}
			v := strings.TrimSpace(vals[0])
			switch k {
			case "format":
				if !isCleanASCIIToken(v, 1, 8, false) {
					return ""
				}
				cleanQ.Set("format", v)
			case "name":
				if !isCleanASCIIToken(v, 1, 16, true) {
					return ""
				}
				cleanQ.Set("name", v)
			default:
				return ""
			}
		}
		u.RawQuery = cleanQ.Encode()
	}
	u.Fragment = ""
	return u.String()
}

func isCleanASCIIToken(s string, minLen, maxLen int, allowUnderscore bool) bool {
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || (allowUnderscore && c == '_')
		if !ok {
			return false
		}
	}
	return true
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
	if hasSignedOrQueryMarkers(clean) {
		clean = ""
	}
	if clean != "" && site == "x" {
		if u, err := url.Parse(clean); err == nil {
			host := strings.ToLower(u.Hostname())
			if host == "pbs.twimg.com" || isForbiddenXMediaHost(host) {
				clean = ""
			}
		}
	}
	if clean != "" {
		return clean
	}
	if rawID == "" {
		return ""
	}
	switch site {
	case "douyin":
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
	case "weibo":
		switch kind {
		case "dynamic":
			return "https://weibo.com/detail/" + rawID
		case "video":
			return "https://weibo.com/tv/show/" + strings.Replace(rawID, "1034_", "1034:", 1)
		case "live":
			return "https://weibo.com/l/wblive/p/show/" + strings.TrimPrefix(rawID, "live:")
		}
	case "x":
		return "https://x.com/i/web/status/" + rawID
	}
	return ""
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
		incomplete := expectedPagesIncomplete(meta)
		belowBest := false
		for _, part := range meta.Parts {
			belowBest = belowBest || part.Available.BestVideo > part.Video.Quality || audioPriority(part.Available.BestAudio) > audioPriority(part.Audio.Quality)
		}
		if meta.UpgradeAvailable || len(meta.Missing) > 0 || incomplete || belowBest {
			d.Dot = "yellow"
			switch {
			case meta.UpgradeAvailable:
				d.Label = "可升级画质"
			case len(meta.Missing) > 0 || incomplete:
				d.Label = "部分缺失"
			default:
				d.Label = "非最高画质"
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

// expectedPagesIncomplete reports whether a page the item expects has no file.
func expectedPagesIncomplete(meta ItemMetadata) bool {
	for _, page := range meta.ExpectedPages {
		found := false
		for _, part := range meta.Parts {
			if part.Page == page && part.File != "" {
				found = true
			}
		}
		if !found {
			return true
		}
	}
	return false
}

// NeedsCompletion reports whether a cached item lacks content that caching
// it again can fill: it is partial, or complete with missing records or
// expected pages without a file. A better quality being available is not
// missing content, and paused or failed items are not counted here.
func NeedsCompletion(meta ItemMetadata) bool {
	switch meta.Status {
	case "partial":
		return true
	case "complete":
		return len(meta.Missing) > 0 || expectedPagesIncomplete(meta)
	}
	return false
}

// WeiboStaticImageSlot describes a cached Weibo static (non-GIF) primary image
// slot that does not yet have a watermark-free companion (images/NN_nowm.*).
type WeiboStaticImageSlot struct {
	PageIdx       int    // index into ItemMetadata.NotePages, or -1 if NotePages is empty
	DisplayIndex  int    // 1-based display ordinal
	PrimaryPath   string // e.g. "images/01.jpg"
	PrimarySHA256 string
	NoWMStem      string // e.g. "images/01_nowm"
	PicID         string // sanitized Weibo pic_id when known offline
	FromOrigin    bool
}

// WeiboStaticImageSlotsMissingNoWM returns all static (non-GIF, non-video)
// primary image slots on a cached Weibo post that currently lack a companion
// image_nowm file (pure offline inspection, 0 network requests, DEC-079).
func WeiboStaticImageSlotsMissingNoWM(meta ItemMetadata) []WeiboStaticImageSlot {
	if !strings.EqualFold(strings.TrimSpace(meta.Site), "weibo") || meta.Kind != "dynamic" || meta.IsArchivedSnapshot() {
		return nil
	}
	if meta.Status != "complete" && meta.Status != "partial" {
		return nil
	}
	mediaByPath := make(map[string]MediaFile, len(meta.Media))
	existingNoWMStems := make(map[string]bool)
	for _, mf := range meta.Media {
		rel := strings.TrimSpace(mf.Path)
		if rel == "" {
			continue
		}
		lower := strings.ToLower(rel)
		mediaByPath[lower] = mf
		if mf.Role == "image_nowm" {
			existingNoWMStems[strings.ToLower(stripFileExt(rel))] = true
		}
	}

	var slots []WeiboStaticImageSlot
	if len(meta.NotePages) > 0 {
		for i, pg := range meta.NotePages {
			imgPath := strings.TrimSpace(pg.Image)
			if imgPath == "" || pg.GIF {
				continue
			}
			kind := strings.ToLower(strings.TrimSpace(pg.Kind))
			if kind == "video" || kind == "gif" || strings.HasSuffix(strings.ToLower(imgPath), ".gif") {
				continue
			}
			mf := mediaByPath[strings.ToLower(imgPath)]
			if mf.GIF {
				continue
			}
			if strings.TrimSpace(pg.NoWatermark) != "" {
				continue
			}
			nowmStem := stripFileExt(imgPath) + "_nowm"
			if existingNoWMStems[strings.ToLower(nowmStem)] {
				continue
			}
			picID := SanitizeMediaToken(pg.MediaID)
			if picID == "" && len(meta.AssetIDs) == len(meta.NotePages) {
				picID = SanitizeMediaToken(meta.AssetIDs[i])
			}
			idx := pg.Index
			if idx <= 0 {
				idx = i + 1
			}
			slots = append(slots, WeiboStaticImageSlot{
				PageIdx:       i,
				DisplayIndex:  idx,
				PrimaryPath:   imgPath,
				PrimarySHA256: mf.SHA256,
				NoWMStem:      nowmStem,
				PicID:         picID,
				FromOrigin:    pg.FromOrigin || mf.FromOrigin,
			})
		}
		return slots
	}

	var imageMedias []MediaFile
	for _, mf := range meta.Media {
		if mf.Role == "image" && strings.TrimSpace(mf.Path) != "" {
			imageMedias = append(imageMedias, mf)
		}
	}
	for i, mf := range imageMedias {
		if mf.GIF || strings.HasSuffix(strings.ToLower(mf.Path), ".gif") {
			continue
		}
		nowmStem := stripFileExt(mf.Path) + "_nowm"
		if existingNoWMStems[strings.ToLower(nowmStem)] {
			continue
		}
		picID := ""
		if len(meta.AssetIDs) == len(imageMedias) {
			picID = SanitizeMediaToken(meta.AssetIDs[i])
		}
		slots = append(slots, WeiboStaticImageSlot{
			PageIdx:       -1,
			DisplayIndex:  i + 1,
			PrimaryPath:   mf.Path,
			PrimarySHA256: mf.SHA256,
			NoWMStem:      nowmStem,
			PicID:         picID,
			FromOrigin:    mf.FromOrigin,
		})
	}
	return slots
}

func stripFileExt(rel string) string {
	slash := strings.LastIndexByte(rel, '/')
	dot := strings.LastIndexByte(rel, '.')
	if dot > slash && dot > 0 {
		return rel[:dot]
	}
	return rel
}

// WeiboNeedsNoWMBackfill reports whether a cached Weibo image post should be
// backfilled for watermark-free (/oslarge/) companion images under DEC-078/DEC-079.
// Items already stamped with CacheStandardVersion >= WeiboCacheStandardVersion are
// skipped even if some images had no /oslarge/ variant or matched primary SHA-256.
func WeiboNeedsNoWMBackfill(meta ItemMetadata, removeWatermark bool) bool {
	if !removeWatermark || meta.CacheStandardVersion >= WeiboCacheStandardVersion {
		return false
	}
	return len(WeiboStaticImageSlotsMissingNoWM(meta)) > 0
}

// WeiboHasAllFastCDNPicIDs reports whether every missing static image slot on a
// Weibo item already carries a persisted pic_id (in NotePages[].MediaID or AssetIDs),
// allowing direct /oslarge/ CDN backfill with zero Weibo API calls.
func WeiboHasAllFastCDNPicIDs(meta ItemMetadata) bool {
	slots := WeiboStaticImageSlotsMissingNoWM(meta)
	if len(slots) == 0 {
		return false
	}
	for _, s := range slots {
		if s.PicID == "" {
			return false
		}
	}
	return true
}

// WeiboIsForwardItem reports whether a cached Weibo item is a forwarded/retweeted
// post (stored under forwards/ or carrying via.reason == "forward" or an Origin).
func WeiboIsForwardItem(meta ItemMetadata) bool {
	if meta.Via != nil && meta.Via.Reason == ViaForward {
		return true
	}
	if meta.Origin != nil || strings.EqualFold(strings.TrimSpace(meta.DynamicType), "DYNAMIC_TYPE_FORWARD") {
		return true
	}
	normDir := strings.ReplaceAll(strings.ToLower(meta.ItemDir), `\`, "/")
	return strings.Contains(normDir, "/forwards/")
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
