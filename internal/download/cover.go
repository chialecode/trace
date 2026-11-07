package download

import (
	"context"
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
		if mediaIsValid(*meta, rel) {
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
