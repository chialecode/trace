package live

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/storage"
)

const maxLiveCoverBytes = 16 << 20 // 16 MiB cover image limit

func (m *Manager) hasVerifiedCover(sess *recordSession) bool {
	m.mu.Lock()
	media := append([]library.MediaFile(nil), sess.itemMeta.Media...)
	itemDir := sess.itemDir
	m.mu.Unlock()
	for _, file := range media {
		if file.Role == "cover" && library.VerifyMediaFile(itemDir, file, true) {
			return true
		}
	}
	return false
}

func (m *Manager) checkCoverTargetWritable(sess *recordSession, siteID site.ID, rel string) (string, error) {
	path, err := library.ResolveItemPath(sess.itemDir, rel)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	owned := false
	for _, file := range sess.itemMeta.Media {
		if file.Path == rel {
			owned = true
			break
		}
	}
	m.mu.Unlock()
	if !owned {
		if _, err := os.Lstat(path); err == nil {
			return "", &site.Error{Site: siteID, Kind: site.ErrKindStorage, Message: "封面目标已存在且未登记，原文件已保留"}
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return path, nil
}

// commitLiveCover writes the cover file atomically BEFORE registering it in
// itemMeta.Media and saving item.json, preserving remote write-order safety
// and protecting any pre-existing unowned user file.
func (m *Manager) commitLiveCover(sess *recordSession, siteID site.ID, rel, sourceURL string, raw []byte) error {
	path, err := m.checkCoverTargetWritable(sess, siteID, rel)
	if err != nil {
		return err
	}
	if err := storage.WriteFileAtomic(path, raw, 0o600); err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	media := library.MediaFile{
		Role:   "cover",
		Path:   rel,
		Bytes:  int64(len(raw)),
		SHA256: hex.EncodeToString(hash[:]),
		Source: sourceURL,
	}
	m.mu.Lock()
	sess.itemMeta.Media = mergeLiveMedia(sess.itemMeta.Media, media)
	clearLiveMissing(&sess.itemMeta, "cover")
	m.mu.Unlock()
	return m.saveSession(sess)
}

func (m *Manager) saveLiveCover(ctx context.Context, sess *recordSession) error {
	m.mu.Lock()
	coverURL := firstNonEmpty(sess.coverURL, m.states[sess.roomID].Cover)
	m.mu.Unlock()
	if coverURL == "" || m.hasVerifiedCover(sess) {
		return nil
	}
	const rel = "cover.jpg"
	if _, err := m.checkCoverTargetWritable(sess, site.Bilibili, rel); err != nil {
		return err
	}
	leave := sess.enterExternal()
	raw, err := m.client.FetchImage(ctx, coverURL)
	leave()
	if err != nil {
		m.mu.Lock()
		addLiveMissing(&sess.itemMeta, "cover", "封面保存失败")
		m.mu.Unlock()
		if liveStopReason(err) != "" {
			return err
		}
		return nil
	}
	return m.commitLiveCover(sess, site.Bilibili, rel, sanitizeLiveURL(coverURL), raw)
}

func (m *Manager) saveDouyinLiveCover(ctx context.Context, sess *recordSession, transport site.MediaTransportSpec) error {
	m.mu.Lock()
	coverURL := strings.TrimSpace(sess.coverURL)
	idleTimeout := m.flvIdleTimeout
	m.mu.Unlock()
	if coverURL == "" || m.hasVerifiedCover(sess) {
		return nil
	}
	if idleTimeout <= 0 {
		idleTimeout = defaultFLVIdleTimeout
	}
	vodSpec := site.DouyinVODTransport(transport.UserAgent, transport.AllowLocalhost)
	if err := vodSpec.ValidateURL(coverURL); err != nil {
		return nil
	}
	httpClient, tr := newLiveTransportClientWithTransport(vodSpec, defaultFLVConnectTimeout)
	defer tr.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return nil
	}
	vodSpec.ApplyHeaders(req)
	resp, err := httpClient.Do(req)
	if err != nil {
		var se *site.Error
		if errors.As(err, &se) && liveStopReason(se) != "" {
			return se
		}
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		se := mapLiveHTTPStatus(site.Douyin, resp.StatusCode)
		if liveStopReason(se) != "" {
			return se
		}
		return nil
	}
	reader := site.WrapIdleReadCloser(resp.Body, idleTimeout, func() error {
		return &site.Error{Site: site.Douyin, Kind: site.ErrKindNetwork, Message: "直播封面下载超时"}
	})
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, maxLiveCoverBytes+1))
	if err != nil || len(raw) < 8 || int64(len(raw)) > maxLiveCoverBytes {
		return nil
	}
	ext := sniffLiveCoverExt(raw, resp.Header.Get("Content-Type"))
	if ext == "" {
		return nil
	}
	return m.commitLiveCover(sess, site.Douyin, "cover"+ext, "", raw)
}

func sniffLiveCoverExt(b []byte, ct string) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ".jpg"
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return ".png"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp"
	case len(b) >= 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))):
		return ".gif"
	}
	mt, _, _ := mime.ParseMediaType(ct)
	switch strings.ToLower(strings.TrimSpace(mt)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	}
	return ""
}
