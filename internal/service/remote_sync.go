package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

var errFileTooLarge = errors.New("file too large")

func readFileLimited(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err == nil && info.Size() > maxBytes {
		return nil, errFileTooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, errFileTooLarge
	}
	return raw, nil
}

// SubscriptionTransferService exports and imports subscription lists
// (docs/design/remote.md §6.4); the same snapshot is what the desktop syncs
// to the remote (§4).
type SubscriptionTransferService interface {
	ExportSubscriptions(path string) (string, error)
	PreviewSubscriptionImport(path string) (subscription.ImportPreview, error)
	ApplySubscriptionImport(path string, choices subscription.ImportChoices) (subscription.ImportPreview, error)
}

var _ SubscriptionTransferService = (*Backend)(nil)

// SubscriptionSnapshot is the current subscriptions, rules and the synced
// part of the settings, stamped with this installation.
func (b *Backend) SubscriptionSnapshot() (subscription.Snapshot, error) {
	snap, err := b.subs.Snapshot()
	if err != nil {
		return subscription.Snapshot{}, err
	}
	set := snapshotSettings(b.settings.Get())
	snap.Settings = &set
	node := b.lib.Node()
	if node.Node != "" {
		snap.Node = &subscription.SnapshotNode{ID: node.Node, Name: node.Name}
	}
	snap.ExportedAt = time.Now().UTC().Format(time.RFC3339)
	return snap, nil
}

func snapshotSettings(s settings.Settings) subscription.SnapshotSettings {
	wbNoWM := s.WeiboRemoveWatermark
	return subscription.SnapshotSettings{
		VideoQuality:         s.VideoQuality,
		CodecOrder:           append([]string(nil), s.CodecOrder...),
		AudioQuality:         s.AudioQuality,
		MultiPart:            s.MultiPart,
		Concurrency:          s.Concurrency,
		Danmaku:              s.Danmaku,
		Subtitles:            s.Subtitles,
		AISubtitles:          s.AISubtitles,
		WeiboRemoveWatermark: &wbNoWM,
		CheckIntervalMin:     s.CheckIntervalMin,
		LiveCheckIntervalMin: s.LiveCheckIntervalMin,
	}
}

// applySnapshotSettings sets the synced settings, top-level and nested
// alike, so Normalize keeps them.
func applySnapshotSettings(s settings.Settings, in subscription.SnapshotSettings) settings.Settings {
	if in.VideoQuality != "" {
		s.VideoQuality, s.Download.VideoQuality = in.VideoQuality, in.VideoQuality
	}
	if len(in.CodecOrder) > 0 {
		s.CodecOrder = append([]string(nil), in.CodecOrder...)
		s.Download.CodecOrder = append([]string(nil), in.CodecOrder...)
		s.Download.PreferredVideoCodec = in.CodecOrder[0]
	}
	if in.AudioQuality != "" {
		s.AudioQuality = in.AudioQuality
	}
	if in.MultiPart != "" {
		s.MultiPart, s.Download.MultiPart = in.MultiPart, in.MultiPart
	}
	if in.Concurrency > 0 {
		s.Concurrency, s.Download.MaxConcurrentTasks = in.Concurrency, in.Concurrency
	}
	s.Danmaku, s.Download.SaveDanmaku = in.Danmaku, in.Danmaku
	s.Subtitles, s.Download.SaveSubtitles = in.Subtitles, in.Subtitles
	s.AISubtitles, s.Download.IncludeAI = in.AISubtitles, in.AISubtitles
	if in.WeiboRemoveWatermark != nil {
		s.WeiboRemoveWatermark = *in.WeiboRemoveWatermark
	}
	if in.CheckIntervalMin > 0 {
		s.CheckIntervalMin, s.Subscription.IntervalMinutes = in.CheckIntervalMin, in.CheckIntervalMin
	}
	if in.LiveCheckIntervalMin > 0 {
		s.LiveCheckIntervalMin = in.LiveCheckIntervalMin
		s.Live.PollIntervalSeconds = in.LiveCheckIntervalMin * 60
	}
	return s
}

// LocalSnapshotHash is the hash of the current subscriptions as they would
// be synced; Settings › 远程 compares it with the remote's.
func (b *Backend) LocalSnapshotHash() string {
	snap, err := b.SubscriptionSnapshot()
	if err != nil {
		return ""
	}
	return snap.Hash()
}

// ExportSubscriptions writes trace-subscriptions.json (no Cookie) to path,
// or into path when it is a folder, and returns the file written.
func (b *Backend) ExportSubscriptions(path string) (string, error) {
	path = snapshotPath(path)
	if path == "" {
		return "", fmt.Errorf("请填写保存位置")
	}
	snap, err := b.SubscriptionSnapshot()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := storage.WriteJSONAtomic(path, snap, 0o600); err != nil {
		return "", err
	}
	subs, accs := snap.Counts()
	b.logInfo(fmt.Sprintf("subscriptions exported: subscriptions=%d accounts=%d", subs, accs))
	return path, nil
}

// snapshotPath is path, or trace-subscriptions.json inside it when path is
// a folder.
func snapshotPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), `"`)
	if path == "" {
		return ""
	}
	if st, err := os.Stat(path); (err == nil && st.IsDir()) || strings.HasSuffix(path, "/") || strings.HasSuffix(path, `\`) {
		return filepath.Join(path, subscription.SnapshotFileName)
	}
	return path
}

func readSnapshotFile(path string) (subscription.Snapshot, error) {
	path = snapshotPath(path)
	raw, err := readFileLimited(path, int64(remote.MaxBodyBytes*8))
	if err != nil {
		if errors.Is(err, errFileTooLarge) {
			return subscription.Snapshot{}, subscription.ErrBadSnapshot
		}
		return subscription.Snapshot{}, fmt.Errorf("读取订阅文件失败：%s", filepath.Base(path))
	}
	return subscription.ParseSnapshot(raw)
}

// PreviewSubscriptionImport reads a trace-subscriptions.json and reports
// what importing it would change; nothing is written.
func (b *Backend) PreviewSubscriptionImport(path string) (subscription.ImportPreview, error) {
	snap, err := readSnapshotFile(path)
	if err != nil {
		return subscription.ImportPreview{}, err
	}
	return b.subs.PreviewImport(snap)
}

// ApplySubscriptionImport imports the file with the user's choices in one
// write; the synced settings in the file are not applied on the desktop.
func (b *Backend) ApplySubscriptionImport(path string, choices subscription.ImportChoices) (subscription.ImportPreview, error) {
	snap, err := readSnapshotFile(path)
	if err != nil {
		return subscription.ImportPreview{}, err
	}
	res, err := b.subs.ApplyImport(snap, choices)
	if err != nil {
		return res, err
	}
	b.logInfo(fmt.Sprintf("subscriptions imported: new=%d existing=%d merge=%v mergeTags=%v", len(res.New), len(res.Existing), choices.MergeExisting, choices.MergeTags))
	if subs, err := b.subs.List(); err == nil {
		for _, s := range subs {
			b.rememberAccountAvatars(s.Accounts)
		}
	}
	b.events.Publish(EventSubscriptionChecked, "imported")
	return res, nil
}

func marshalSnapshot(snap subscription.Snapshot) ([]byte, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	if len(raw) > remote.MaxBodyBytes {
		return nil, fmt.Errorf("订阅太多，同步内容超过 1 MiB")
	}
	return raw, nil
}

// syncState is what the remote remembers of the last subscription sync and
// Cookie pushes (<data>/remote-sync.json).
type syncState struct {
	Hash           string            `json:"hash,omitempty"`
	SyncedAt       string            `json:"syncedAt,omitempty"`
	Subscriptions  int               `json:"subscriptions"`
	Accounts       int               `json:"accounts"`
	CookieSyncedAt map[string]string `json:"cookieSyncedAt,omitempty"`
}

const syncStateFile = "remote-sync.json"

func (b *Backend) loadSyncStateLocked() syncState {
	var st syncState
	raw, err := os.ReadFile(filepath.Join(b.loc.DataDir, syncStateFile))
	if err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func (b *Backend) loadSyncState() syncState {
	b.syncStateMu.Lock()
	defer b.syncStateMu.Unlock()
	return b.loadSyncStateLocked()
}

func (b *Backend) mutateSyncState(fn func(*syncState)) (syncState, error) {
	b.syncStateMu.Lock()
	defer b.syncStateMu.Unlock()
	st := b.loadSyncStateLocked()
	fn(&st)
	if err := storage.WriteJSONAtomic(filepath.Join(b.loc.DataDir, syncStateFile), st, 0o600); err != nil {
		return syncState{}, err
	}
	return st, nil
}
