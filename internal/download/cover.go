package download

import (
	"context"
	"os"

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
		break
	}
	saved, err := m.downloadBilibiliImageStaged(ctx, task, meta.ItemDir, *meta, true, "cover", rel, coverURL, 0, 0)
	if err != nil {
		addMissing(meta, "cover", "封面保存失败 ("+bilibili.KindOf(err)+")")
		if isStopError(err) || isStorageError(err) || ctx.Err() != nil {
			return err
		}
		return nil
	}
	meta.Media = mergeMediaFiles(meta.Media, []library.MediaFile{*saved})
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
