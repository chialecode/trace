package live

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

func (m *Manager) saveLiveCover(ctx context.Context, session *recordSession) error {
	m.mu.Lock()
	url := m.states[session.roomID].Cover
	m.mu.Unlock()
	if url == "" {
		return nil
	}
	for _, file := range session.itemMeta.Media {
		if file.Role == "cover" && library.VerifyMediaFile(session.itemDir, file, true) {
			return nil
		}
	}
	const rel = "cover.jpg"
	path, err := library.ResolveItemPath(session.itemDir, rel)
	if err != nil {
		return err
	}
	owned := false
	for _, file := range session.itemMeta.Media {
		owned = owned || file.Path == rel
	}
	if !owned {
		if _, err := os.Lstat(path); err == nil {
			return &bilibili.SiteError{Kind: "storage", Message: "封面目标已存在且未登记，原文件已保留"}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	raw, err := m.client.FetchImage(ctx, url)
	if err != nil {
		addLiveMissing(&session.itemMeta, "cover", "封面保存失败")
		if liveStopReason(err) != "" {
			return err
		}
		return nil
	}
	if !owned {
		session.itemMeta.Media = mergeLiveMedia(session.itemMeta.Media, library.MediaFile{Role: "cover", Path: rel})
		if err := m.saveSession(session); err != nil {
			return err
		}
	}
	if err := storage.WriteFileAtomic(path, raw, 0600); err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	session.itemMeta.Media = mergeLiveMedia(session.itemMeta.Media, library.MediaFile{Role: "cover", Path: rel, Bytes: int64(len(raw)), SHA256: hex.EncodeToString(hash[:]), Source: url})
	clearLiveMissing(&session.itemMeta, "cover")
	return m.saveSession(session)
}
