package companionapi

// StatusResponse is GET /api/v1/status (docs/design/mobile.md §5.2, FR-MOB-017).
type StatusResponse struct {
	Node             string           `json:"node"`
	Name             string           `json:"name"`
	Version          string           `json:"version"`
	Channel          string           `json:"channel"`
	Protocol         string           `json:"protocol"`
	StartedAt        string           `json:"startedAt"`
	UptimeSec        int64            `json:"uptimeSec"`
	Now              string           `json:"now"`
	LoggedIn         bool             `json:"loggedIn"`
	Sites            []SiteAuthStatus `json:"sites"`
	Disk             DiskSpace        `json:"disk"`
	DownloadBadge    int              `json:"downloadBadge"`
	ActiveRecordings int              `json:"activeRecordings"`
	PendingCount     int              `json:"pendingCount"`
}

// SiteAuthStatus summarizes one site's desktop login status (read-only).
type SiteAuthStatus struct {
	Site        string `json:"site"`
	DisplayName string `json:"displayName"`
	State       string `json:"state"` // "logged_in", "logged_out", "expired", "unknown"
	UID         string `json:"uid,omitempty"`
	Uname       string `json:"uname,omitempty"`
}

// DiskSpace reports the cache location's free and total bytes.
type DiskSpace struct {
	FreeBytes  uint64 `json:"freeBytes"`
	TotalBytes uint64 `json:"totalBytes"`
}

// FeedCursor is a keyset pagination cursor (pubTs, key).
type FeedCursor struct {
	PubTs int64  `json:"pubTs"`
	Key   string `json:"key"`
}

// FeedPageResponse is GET /api/v1/feed (docs/design/mobile.md §5.2, FR-MOB-011).
type FeedPageResponse struct {
	Items      []FeedItem  `json:"items"`
	NextCursor *FeedCursor `json:"nextCursor,omitempty"`
	PrevCursor *FeedCursor `json:"prevCursor,omitempty"`
	HasMore    bool        `json:"hasMore"`
}

// FeedItem is one cached post/video/live-recording entry in the mobile home feed.
// All media references are relative paths for GET /api/v1/media/{itemKey}/{relPath}.
type FeedItem struct {
	FeedKey          string              `json:"feedKey"`
	ItemKey          string              `json:"itemKey"`
	Site             string              `json:"site"`
	Kind             string              `json:"kind"` // "video", "dynamic", "live"
	Status           string              `json:"status,omitempty"`
	DynamicType      string              `json:"dynamicType,omitempty"`
	TypeLabel        string              `json:"typeLabel,omitempty"`
	AuthorUID        string              `json:"authorUid"`
	AuthorName       string              `json:"authorName"`
	AuthorUniqueID   string              `json:"authorUniqueId,omitempty"`
	AuthorAvatarPath string              `json:"authorAvatarPath,omitempty"`
	SubID            string              `json:"subId,omitempty"`
	SubName          string              `json:"subName,omitempty"`
	Title            string              `json:"title"`
	Text             string              `json:"text,omitempty"`
	Desc             string              `json:"desc,omitempty"`
	CanonicalURL     string              `json:"canonicalUrl,omitempty"`
	Links            []TextLink          `json:"links,omitempty"`
	PubTs            int64               `json:"pubTs"`
	Duration         int                 `json:"duration,omitempty"`
	CoverRelPath     string              `json:"coverRelPath,omitempty"`
	VideoRelPath     string              `json:"videoRelPath,omitempty"`
	QualitySummary   string              `json:"qualitySummary,omitempty"`
	Images           []FeedImage         `json:"images,omitempty"`
	NotePages        []NotePageDTO       `json:"notePages,omitempty"`
	Parts            []VideoPartDTO      `json:"parts,omitempty"`
	Music            *MusicDTO           `json:"music,omitempty"`
	Live             *LiveInfoDTO        `json:"live,omitempty"`
	Via              *ItemViaDTO         `json:"via,omitempty"`
	Origin           *ItemOriginDTO      `json:"origin,omitempty"`
	InReplyTo        *ItemReplyTargetDTO `json:"inReplyTo,omitempty"`
	Card             *ItemRichCardDTO    `json:"card,omitempty"`
	Thread           []ThreadPartDTO     `json:"thread,omitempty"`
	IsForward        bool                `json:"isForward,omitempty"`
	TotalBytes       int64               `json:"totalBytes,omitempty"`
}

// FeedImage is a sanitized image reference on a feed post.
type FeedImage struct {
	Path        string `json:"path"`
	NoWatermark string `json:"noWatermark,omitempty"`
	Motion      string `json:"motion,omitempty"`
	GIF         bool   `json:"gif,omitempty"`
	FromOrigin  bool   `json:"fromOrigin,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
}

// TextLink is a human-readable hyperlink label paired with a safe HTTP(S) URL.
type TextLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// FeedSubscriptionsResponse is GET /api/v1/feed/subscriptions (FR-MOB-011).
type FeedSubscriptionsResponse struct {
	Items []FeedSubscriptionRow `json:"items"`
}

// FeedSubscriptionRow is one avatar-strip entry on the home feed.
type FeedSubscriptionRow struct {
	SubID       string `json:"subId"`
	Name        string `json:"name"`
	AvatarPath  string `json:"avatarPath,omitempty"`
	Live        bool   `json:"live"`
	Recording   bool   `json:"recording,omitempty"`
	Unread      bool   `json:"unread"`
	LatestPubTs int64  `json:"latestPubTs,omitempty"`
}

// FeedSeenRequest is POST /api/v1/feed/seen (shared read state with desktop, DEC-085 ⑥).
type FeedSeenRequest struct {
	SubID  string `json:"subId"`
	SeenTs int64  `json:"seenTs,omitempty"`
}

// HomePosition is one site filter's saved scroll position for this mobile device.
type HomePosition struct {
	Site     string `json:"site,omitempty"`
	Key      string `json:"key"`
	PubTs    int64  `json:"pubTs"`
	OffsetPx int    `json:"offsetPx"`
}

// HomePositionsResponse is GET /api/v1/feed/position.
type HomePositionsResponse struct {
	Positions map[string]HomePosition `json:"positions"`
}

// LibraryListResponse is GET /api/v1/library (FR-MOB-012).
type LibraryListResponse struct {
	Items      []LibraryItemSummary   `json:"items"`
	Total      int                    `json:"total"`
	HasMore    bool                   `json:"hasMore"`
	NextOffset int                    `json:"nextOffset,omitempty"`
	Authors    []LibraryAuthorSummary `json:"authors,omitempty"`
}

// LibraryAuthorSummary describes one subscription or unsubscribed author in the library filter.
type LibraryAuthorSummary struct {
	Kind       string `json:"kind"` // "subscription" or "author"
	SubID      string `json:"subId,omitempty"`
	Site       string `json:"site,omitempty"`
	UID        string `json:"uid,omitempty"`
	Name       string `json:"name"`
	AvatarPath string `json:"avatarPath,omitempty"`
	ItemCount  int    `json:"itemCount"`
	TotalBytes int64  `json:"totalBytes"`
}

// LibraryItemSummary is one grid card in the mobile media library.
type LibraryItemSummary struct {
	ItemKey        string `json:"itemKey"`
	Site           string `json:"site"`
	Kind           string `json:"kind"` // "video", "dynamic", "live"
	Status         string `json:"status"`
	Title          string `json:"title"`
	AuthorUID      string `json:"authorUid"`
	AuthorName     string `json:"authorName"`
	SubID          string `json:"subId,omitempty"`
	SubName        string `json:"subName,omitempty"`
	PubTs          int64  `json:"pubTs"`
	Duration       int    `json:"duration,omitempty"`
	CoverRelPath   string `json:"coverRelPath,omitempty"`
	PrimaryRelPath string `json:"primaryRelPath,omitempty"`
	PrimaryKind    string `json:"primaryKind,omitempty"` // "image", "video", "live"
	ImageCount     int    `json:"imageCount,omitempty"`
	PartCount      int    `json:"partCount,omitempty"`
	QualitySummary string `json:"qualitySummary,omitempty"`
	IsForward      bool   `json:"isForward,omitempty"`
	TotalBytes     int64  `json:"totalBytes,omitempty"`
}

// LibraryItemDetail is GET /api/v1/library/items/{key} (read-only, no local absolute paths).
type LibraryItemDetail struct {
	ItemKey          string              `json:"itemKey"`
	ID               string              `json:"id"`
	Site             string              `json:"site"`
	Kind             string              `json:"kind"`
	Status           string              `json:"status"`
	Title            string              `json:"title"`
	Desc             string              `json:"desc,omitempty"`
	Text             string              `json:"text,omitempty"`
	DynamicType      string              `json:"dynamicType,omitempty"`
	TypeLabel        string              `json:"typeLabel,omitempty"`
	CanonicalURL     string              `json:"canonicalUrl,omitempty"`
	PubTs            int64               `json:"pubTs"`
	CachedAt         string              `json:"cachedAt,omitempty"`
	Duration         int                 `json:"duration,omitempty"`
	AuthorUID        string              `json:"authorUid"`
	AuthorName       string              `json:"authorName"`
	AuthorUniqueID   string              `json:"authorUniqueId,omitempty"`
	AuthorAvatarPath string              `json:"authorAvatarPath,omitempty"`
	SubID            string              `json:"subId,omitempty"`
	SubName          string              `json:"subName,omitempty"`
	Media            []MediaFileDTO      `json:"media,omitempty"`
	Parts            []VideoPartDTO      `json:"parts,omitempty"`
	VideoInfo        *VideoInfoDTO       `json:"videoInfo,omitempty"`
	NotePages        []NotePageDTO       `json:"notePages,omitempty"`
	Music            *MusicDTO           `json:"music,omitempty"`
	Live             *LiveInfoDTO        `json:"live,omitempty"`
	Missing          []MissingItemDTO    `json:"missing,omitempty"`
	Via              *ItemViaDTO         `json:"via,omitempty"`
	Origin           *ItemOriginDTO      `json:"origin,omitempty"`
	InReplyTo        *ItemReplyTargetDTO `json:"inReplyTo,omitempty"`
	Card             *ItemRichCardDTO    `json:"card,omitempty"`
	Thread           []ThreadPartDTO     `json:"thread,omitempty"`
	Links            []TextLink          `json:"links,omitempty"`
	IsForward        bool                `json:"isForward,omitempty"`
	ArchivedOf       string              `json:"archivedOf,omitempty"`
	TotalBytes       int64               `json:"totalBytes,omitempty"`
}

// MediaFileDTO describes one registered relative media file in an item.
type MediaFileDTO struct {
	Role       string `json:"role"`
	Path       string `json:"path"`
	GIF        bool   `json:"gif,omitempty"`
	FromOrigin bool   `json:"fromOrigin,omitempty"`
	Bytes      int64  `json:"bytes"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Duration   int    `json:"duration,omitempty"`
	Motion     string `json:"motion,omitempty"`
}

// VideoInfoDTO describes single-file video quality/codec metadata.
type VideoInfoDTO struct {
	File        string `json:"file,omitempty"`
	QualityName string `json:"qualityName,omitempty"`
	Codec       string `json:"codec,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	FPS         int    `json:"fps,omitempty"`
	Bitrate     int64  `json:"bitrate,omitempty"`
}

// VideoPartDTO describes one video part (分 P).
type VideoPartDTO struct {
	Page        int    `json:"page"`
	Title       string `json:"title"`
	Duration    int    `json:"duration"`
	File        string `json:"file"`
	QualityName string `json:"qualityName,omitempty"`
	Codecs      string `json:"codecs,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	AudioName   string `json:"audioName,omitempty"`
	AudioFile   string `json:"audioFile,omitempty"`
}

// NotePageDTO describes one ordered page in a Douyin/Weibo/X note/post.
type NotePageDTO struct {
	Index       int           `json:"index"`
	Kind        string        `json:"kind"` // "image", "motion", "video", "unknown"
	GIF         bool          `json:"gif,omitempty"`
	FromOrigin  bool          `json:"fromOrigin,omitempty"`
	Image       string        `json:"image,omitempty"`
	NoWatermark string        `json:"noWatermark,omitempty"`
	Cover       string        `json:"cover,omitempty"`
	Motion      string        `json:"motion,omitempty"`
	Video       string        `json:"video,omitempty"`
	Width       int           `json:"width,omitempty"`
	Height      int           `json:"height,omitempty"`
	Duration    int           `json:"duration,omitempty"`
	VideoInfo   *VideoInfoDTO `json:"videoInfo,omitempty"`
}

// MusicDTO describes background audio attached to a note.
type MusicDTO struct {
	Title    string `json:"title,omitempty"`
	Author   string `json:"author,omitempty"`
	Duration int    `json:"duration,omitempty"`
	File     string `json:"file,omitempty"`
	Cover    string `json:"cover,omitempty"`
	Original bool   `json:"original,omitempty"`
}

// LiveInfoDTO describes a cached live recording's metadata and segments.
type LiveInfoDTO struct {
	RoomID      string           `json:"roomId,omitempty"`
	WebRID      string           `json:"webRid,omitempty"`
	LiveSince   int64            `json:"liveSince,omitempty"`
	StartedAt   string           `json:"startedAt,omitempty"`
	EndedAt     string           `json:"endedAt,omitempty"`
	QualityName string           `json:"qualityName,omitempty"`
	Codec       string           `json:"codec,omitempty"`
	Segments    []LiveSegmentDTO `json:"segments,omitempty"`
	Incidents   []LiveIncident   `json:"incidents,omitempty"`
}

// LiveSegmentDTO describes one finalized MP4 segment of a live recording.
type LiveSegmentDTO struct {
	File        string  `json:"file"`
	Bytes       int64   `json:"bytes"`
	DurationSec float64 `json:"durationSec"`
	Codec       string  `json:"codec,omitempty"`
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	QualityName string  `json:"qualityName,omitempty"`
}

// MissingItemDTO describes one missing component of a partial item.
type MissingItemDTO struct {
	Item   string `json:"item"`
	Reason string `json:"reason"`
}

// ItemViaDTO records forward/collaboration attribution.
type ItemViaDTO struct {
	Reason string `json:"reason"`
	Site   string `json:"site,omitempty"`
	UID    string `json:"uid"`
	Name   string `json:"name,omitempty"`
}

// ItemRichCardDTO describes a non-media rich card summary.
type ItemRichCardDTO struct {
	Kind  string `json:"kind,omitempty"`
	Label string `json:"label,omitempty"`
	Title string `json:"title,omitempty"`
	Desc  string `json:"desc,omitempty"`
	URL   string `json:"url,omitempty"`
}

// ItemReplyTargetDTO describes sanitized reply target identifiers (DEC-091).
type ItemReplyTargetDTO struct {
	StatusID   string `json:"statusId,omitempty"`
	UserID     string `json:"userId,omitempty"`
	ScreenName string `json:"screenName,omitempty"`
}

// ThreadPartDTO describes one segment of an author self-reply thread.
type ThreadPartDTO struct {
	ID           string         `json:"id,omitempty"`
	PubTs        int64          `json:"pubTs,omitempty"`
	Text         string         `json:"text,omitempty"`
	Links        []TextLink     `json:"links,omitempty"`
	MediaIndexes []int          `json:"mediaIndexes,omitempty"`
	Origin       *ItemOriginDTO `json:"origin,omitempty"`
}

// ItemOriginDTO describes a forwarded/quoted original post snapshot (at most 1 nested level).
type ItemOriginDTO struct {
	Site              string              `json:"site,omitempty"`
	ID                string              `json:"id,omitempty"`
	CanonicalURL      string              `json:"canonicalUrl,omitempty"`
	AuthorUID         string              `json:"authorUid,omitempty"`
	AuthorUniqueID    string              `json:"authorUniqueId,omitempty"`
	AuthorName        string              `json:"authorName,omitempty"`
	PubTs             int64               `json:"pubTs,omitempty"`
	Title             string              `json:"title,omitempty"`
	Text              string              `json:"text,omitempty"`
	Links             []TextLink          `json:"links,omitempty"`
	Card              *ItemRichCardDTO    `json:"card,omitempty"`
	InReplyTo         *ItemReplyTargetDTO `json:"inReplyTo,omitempty"`
	Origin            *ItemOriginDTO      `json:"origin,omitempty"`
	Unavailable       bool                `json:"unavailable,omitempty"`
	UnavailableReason string              `json:"unavailableReason,omitempty"`
	VideoFromOrigin   bool                `json:"videoFromOrigin,omitempty"`
}

// SubscriptionsResponse is GET /api/v1/subscriptions (FR-MOB-015).
type SubscriptionsResponse struct {
	Items []SubscriptionDTO `json:"items"`
}

// SubscriptionDetailResponse is GET /api/v1/subscriptions/{id}.
type SubscriptionDetailResponse struct {
	Subscription SubscriptionDTO `json:"subscription"`
}

// AllAccountsResponse is GET /api/v1/subscriptions/accounts.
type AllAccountsResponse struct {
	Items []SubscriptionAccountDTO `json:"items"`
}

// CustomLinkDTO is one user-configured external link on a subscription.
type CustomLinkDTO struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// SubscriptionDTO summarizes one subscription and its linked accounts.
type SubscriptionDTO struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Note        string                   `json:"note,omitempty"`
	AvatarPath  string                   `json:"avatarPath,omitempty"`
	PresetLabel string                   `json:"presetLabel,omitempty"`
	Tags        []string                 `json:"tags,omitempty"`
	TagLabels   []string                 `json:"tagLabels,omitempty"`
	Links       []CustomLinkDTO          `json:"links,omitempty"`
	Accounts    []SubscriptionAccountDTO `json:"accounts,omitempty"`
	Live        bool                     `json:"live"`
	Recording   bool                     `json:"recording"`
	CreatedAt   string                   `json:"createdAt,omitempty"`
}

// SubscriptionAccountDTO describes one linked account with its cached live status.
type SubscriptionAccountDTO struct {
	SubID         string `json:"subId"`
	SubName       string `json:"subName"`
	Site          string `json:"site"`
	UID           string `json:"uid"`
	AccountKey    string `json:"accountKey"`
	UniqueID      string `json:"uniqueId,omitempty"`
	Name          string `json:"name"`
	AvatarPath    string `json:"avatarPath,omitempty"`
	TotalWorks    int    `json:"totalWorks,omitempty"`
	CachedWorks   int    `json:"cachedWorks,omitempty"`
	LiveSupported bool   `json:"liveSupported"`
	LiveStatus    int    `json:"liveStatus,omitempty"` // 0=offline, 1=live, 2=carousel
	LiveTitle     string `json:"liveTitle,omitempty"`
	LiveStatusTxt string `json:"liveStatusText,omitempty"`
	Recording     bool   `json:"recording,omitempty"`
	RecordingKey  string `json:"recordingKey,omitempty"`
	QualityName   string `json:"qualityName,omitempty"`
	Codec         string `json:"codec,omitempty"`
}

// DownloadTasksResponse is GET /api/v1/tasks/downloads (FR-MOB-016).
type DownloadTasksResponse struct {
	Items []DownloadTaskDTO `json:"items"`
}

// DownloadTaskDTO is one download task (never carries local ItemDir or signed URLs).
type DownloadTaskDTO struct {
	TaskID          string  `json:"taskId"`
	Site            string  `json:"site"`
	Kind            string  `json:"kind"`
	Source          string  `json:"source,omitempty"`
	TargetID        string  `json:"targetId"`
	Title           string  `json:"title"`
	PartTitle       string  `json:"partTitle,omitempty"`
	AuthorUID       string  `json:"authorUid,omitempty"`
	AuthorName      string  `json:"authorName,omitempty"`
	Status          string  `json:"status"`
	PauseReason     string  `json:"pauseReason,omitempty"`
	ErrorKind       string  `json:"errorKind,omitempty"`
	ErrorMessage    string  `json:"errorMessage,omitempty"`
	QualityHint     string  `json:"qualityHint,omitempty"`
	BytesDownloaded int64   `json:"bytesDownloaded"`
	TotalBytes      int64   `json:"totalBytes"`
	SpeedBps        int64   `json:"speedBps,omitempty"`
	Progress        float64 `json:"progress"`
	ItemKey         string  `json:"itemKey,omitempty"`
	CoverRelPath    string  `json:"coverRelPath,omitempty"`
	CreatedAt       string  `json:"createdAt"`
	UpdatedAt       string  `json:"updatedAt"`
}

// DownloadTaskFileDTO is one file slot in GET /api/v1/tasks/downloads/{id}.
type DownloadTaskFileDTO struct {
	Role            string `json:"role"`
	Path            string `json:"path"`
	Status          string `json:"status"`
	DownloadedBytes int64  `json:"downloadedBytes"`
	TotalBytes      int64  `json:"totalBytes"`
	SpeedBps        int64  `json:"speedBps"`
}

// DownloadTaskAttemptDTO is one sanitized failed segment attempt (host only).
type DownloadTaskAttemptDTO struct {
	Time       string `json:"time"`
	File       string `json:"file"`
	Offset     int64  `json:"offset"`
	Host       string `json:"host"`
	Attempt    int    `json:"attempt"`
	ErrorKind  string `json:"errorKind"`
	HTTPStatus int    `json:"httpStatus"`
}

// DownloadTaskDetailResponse is GET /api/v1/tasks/downloads/{id}.
type DownloadTaskDetailResponse struct {
	Task     DownloadTaskDTO          `json:"task"`
	Files    []DownloadTaskFileDTO    `json:"files,omitempty"`
	Attempts []DownloadTaskAttemptDTO `json:"attempts,omitempty"`
}

// CacheQueuesResponse is GET /api/v1/tasks/queues.
type CacheQueuesResponse struct {
	Items []CacheQueueDTO `json:"items"`
}

// CacheQueueRecentDTO is one recently cached item under a queue run.
type CacheQueueRecentDTO struct {
	ItemKey  string `json:"itemKey"`
	Title    string `json:"title"`
	CachedAt string `json:"cachedAt"`
}

// CacheQueueDTO is one background cache queue task.
type CacheQueueDTO struct {
	ID            string                `json:"id"`
	Site          string                `json:"site"`
	User          string                `json:"user"`
	UserName      string                `json:"userName"`
	AvatarPath    string                `json:"avatarPath,omitempty"`
	List          string                `json:"list"`
	ListLabel     string                `json:"listLabel"`
	Kind          string                `json:"kind,omitempty"`
	Origin        string                `json:"origin,omitempty"`
	Status        string                `json:"status"`
	StatusText    string                `json:"statusText"`
	PauseReason   string                `json:"pauseReason,omitempty"`
	StopReason    string                `json:"stopReason,omitempty"`
	Note          string                `json:"note,omitempty"`
	CachedCount   int                   `json:"cachedCount"`
	SkippedCount  int                   `json:"skippedCount"`
	ActiveItemKey string                `json:"activeItemKey,omitempty"`
	ActiveTitle   string                `json:"activeTitle,omitempty"`
	RecentItems   []CacheQueueRecentDTO `json:"recentItems,omitempty"`
	NextRunAt     string                `json:"nextRunAt,omitempty"`
	CreatedAt     string                `json:"createdAt"`
	UpdatedAt     string                `json:"updatedAt"`
}

// LiveRecordingsResponse is GET /api/v1/live/recordings.
type LiveRecordingsResponse struct {
	Items []LiveRecordingDTO `json:"items"`
}

// LiveRecordingDTO describes one active or recently finished live recording (no local paths).
type LiveRecordingDTO struct {
	RecordingKey string         `json:"recordingKey"`
	AccountKey   string         `json:"accountKey"`
	Site         string         `json:"site"`
	UID          string         `json:"uid"`
	RoomID       string         `json:"roomId,omitempty"`
	Uname        string         `json:"uname"`
	AvatarPath   string         `json:"avatarPath,omitempty"`
	Title        string         `json:"title,omitempty"`
	StatusText   string         `json:"statusText,omitempty"`
	QualityName  string         `json:"qualityName,omitempty"`
	Codec        string         `json:"codec,omitempty"`
	Recording    bool           `json:"recording"`
	DurationSec  float64        `json:"durationSec,omitempty"`
	SpeedBps     int64          `json:"speedBps,omitempty"`
	Bytes        int64          `json:"bytes,omitempty"`
	Segments     int            `json:"segments,omitempty"`
	ItemKey      string         `json:"itemKey,omitempty"`
	Incidents    []LiveIncident `json:"incidents,omitempty"`
	StopReason   string         `json:"stopReason,omitempty"`
	LastError    string         `json:"lastError,omitempty"`
	StartedAt    string         `json:"startedAt,omitempty"`
	EndedAt      string         `json:"endedAt,omitempty"`
	UpdatedAt    string         `json:"updatedAt,omitempty"`
}

// LiveIncident describes one recording anomaly (no stream URLs or paths).
type LiveIncident struct {
	Kind         string  `json:"kind"`
	At           string  `json:"at"`
	EndedAt      string  `json:"endedAt,omitempty"`
	DurationSec  float64 `json:"durationSec,omitempty"`
	LostSegments int     `json:"lostSegments,omitempty"`
	Count        int     `json:"count,omitempty"`
	Part         string  `json:"part,omitempty"`
	Reason       string  `json:"reason,omitempty"`
}

// LiveIncidentsResponse is GET /api/v1/live/incidents (recent 7-day sessions with incidents).
type LiveIncidentsResponse struct {
	Items []LiveIncidentSessionDTO `json:"items"`
}

// LiveIncidentSessionDTO groups incidents by live recording session.
type LiveIncidentSessionDTO struct {
	ItemKey      string         `json:"itemKey,omitempty"`
	RecordingKey string         `json:"recordingKey,omitempty"`
	Site         string         `json:"site"`
	UID          string         `json:"uid,omitempty"`
	Uname        string         `json:"uname"`
	Title        string         `json:"title,omitempty"`
	Recording    bool           `json:"recording"`
	StartedAt    string         `json:"startedAt,omitempty"`
	EndedAt      string         `json:"endedAt,omitempty"`
	Incidents    []LiveIncident `json:"incidents"`
}

// CheckPlanResponse is GET /api/v1/checks/plan.
type CheckPlanResponse struct {
	Items    []CheckPlanRowDTO `json:"items"`
	Checking bool              `json:"checking,omitempty"`
	Status   string            `json:"status,omitempty"`
}

// CheckPlanRowDTO is one subscribed account row in the update check plan.
type CheckPlanRowDTO struct {
	SubID         string `json:"subId"`
	SubName       string `json:"subName"`
	Site          string `json:"site"`
	UID           string `json:"uid"`
	Name          string `json:"name"`
	AutoRecord    bool   `json:"autoRecord,omitempty"`
	LiveNote      string `json:"liveNote,omitempty"`
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`
	LastKind      string `json:"lastKind,omitempty"`
	Note          string `json:"note,omitempty"`
	Plan          string `json:"plan"`
	NextAt        string `json:"nextAt,omitempty"`
	AfterBackoff  bool   `json:"afterBackoff,omitempty"`
	LiveCheckedAt string `json:"liveCheckedAt,omitempty"`
	LiveEverySec  int    `json:"liveEverySec,omitempty"`
}

// ConflictsResponse is GET /api/v1/conflicts (read-only on mobile MVP).
type ConflictsResponse struct {
	Items []ConflictDTO `json:"items"`
}

// ConflictDTO is one pending/failed V1-vs-V2 cache conflict.
type ConflictDTO struct {
	ID            string `json:"id"`
	ItemKey       string `json:"itemKey"`
	Site          string `json:"site"`
	Kind          string `json:"kind"`
	AuthorName    string `json:"authorName,omitempty"`
	Title         string `json:"title"`
	DetectedAt    string `json:"detectedAt"`
	ReasonKind    string `json:"reasonKind"`
	ReasonLabel   string `json:"reasonLabel"`
	LocalSummary  string `json:"localSummary"`
	RemoteSummary string `json:"remoteSummary"`
	State         string `json:"state"`
	LastError     string `json:"lastError,omitempty"`
}

// TransfersResponse is GET /api/v1/transfers (read-only on mobile MVP).
type TransfersResponse struct {
	Items []TransferJobDTO `json:"items"`
}

// TransferJobDTO summarizes one desktop import/export job without exposing absolute paths.
type TransferJobDTO struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // "import" or "export"
	Status     string `json:"status"`
	DoneItems  int    `json:"doneItems"`
	TotalItems int    `json:"totalItems"`
	DoneBytes  int64  `json:"doneBytes"`
	TotalBytes int64  `json:"totalBytes"`
	SpeedBps   int64  `json:"speedBps,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// Control endpoint request and response types (docs/design/mobile.md §5.3).

// StartLiveRequest is POST /api/v1/live/start.
type StartLiveRequest struct {
	AccountKey string `json:"accountKey"`
}

// CheckRequest is POST /api/v1/checks (empty SubIDs checks all subscriptions).
type CheckRequest struct {
	SubIDs []string `json:"subIds,omitempty"`
}

// CheckAccountRequest is POST /api/v1/checks/account.
type CheckAccountRequest struct {
	SubID string `json:"subId"`
	Site  string `json:"site"`
	UID   string `json:"uid"`
}

// ActionResponse is returned by control endpoints that perform a state change.
type ActionResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Count   int    `json:"count,omitempty"`
}
