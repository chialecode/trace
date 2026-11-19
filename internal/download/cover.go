package download

import (
	"context"
	"os"
	"path/filepath"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func (m *Manager) saveVideoCover(ctx context.Context, task *Task, meta *library.ItemMetadata, coverURL string, cfg settings.DownloadSettings) error {
	if !cfg.SaveCover || coverURL == "" {
		clearMissing(meta, "cover")
		return nil
	}
	rel := "cover" + guessImageExt(coverURL)
	for _, file := range meta.Media {
		if file.Role != "cover" {
			continue
		}
		rel = file.Path
		if m.mediaAlreadyCompleted(*meta, file) {
			clearMissing(meta, "cover")
			return nil
		}
		break
	}
	m.setFileProgress(task, "cover", rel, "downloading", 0, 0, 0)
	n, hash, err := m.downloadFileWithRange(ctx, []string{coverURL}, filepath.Join(meta.ItemDir, rel), task)
	if err != nil {
		m.setFileProgress(task, "cover", rel, "failed", 0, 0, 0)
		addMissing(meta, "cover", "封面保存失败 ("+bilibili.KindOf(err)+")")
		if isStopError(err) || isStorageError(err) || ctx.Err() != nil {
			return err
		}
		return nil
	}
	meta.Media = mergeMediaFiles(meta.Media, []library.MediaFile{{Role: "cover", Path: rel, Bytes: n, SHA256: hash, Source: coverURL}})
	clearMissing(meta, "cover")
	return m.saveVideoManifest(task, meta)
}

// mediaAlreadyCompleted reports whether a registered manifest file is valid on
// disk, or was already completed and later moved away in remote/export mode.
func (m *Manager) mediaAlreadyCompleted(meta library.ItemMetadata, file library.MediaFile) bool {
	if file.Path == "" {
		return false
	}
	if library.VerifyMediaFile(meta.ItemDir, file, true) {
		return true
	}
	if m.lib != nil && m.lib.AllowMovedFiles() && file.Bytes > 0 && file.SHA256 != "" {
		if full, err := library.ResolveItemPath(meta.ItemDir, file.Path); err == nil {
			if _, statErr := os.Stat(full); os.IsNotExist(statErr) {
				return true
			}
		}
	}
	return false
}
