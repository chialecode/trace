package companion

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/site"
)

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request, rest string) {
	// rest is "<escapedKey>/<relPath...>"
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash >= len(rest)-1 {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "媒体路径不正确", nil)
		return
	}
	itemKey, err := url.PathUnescape(rest[:slash])
	if err != nil || strings.TrimSpace(itemKey) == "" {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "条目键不正确", nil)
		return
	}
	rawRel := rest[slash+1:]
	parts := strings.Split(rawRel, "/")
	unescaped := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		u, uErr := url.PathUnescape(p)
		if uErr != nil {
			writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "相对路径不正确", nil)
			return
		}
		unescaped = append(unescaped, u)
	}
	relPath := strings.Join(unescaped, "/")
	if relPath == "" {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "缺少媒体文件相对路径", nil)
		return
	}

	// Enforce INV-08 whitelist via LibraryService.MediaPath: rejects "..", symlinks/junctions,
	// unregistered files, and cross-item traversal.
	fullPath, err := s.backend.MediaPath(itemKey, relPath)
	if err != nil {
		// Never leak full local filesystem paths in errors (NFR-MOB-SEC-003).
		s.logf("media denied: key=%s err_kind=%s", itemKey, site.KindOf(err))
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "媒体文件不可访问或不存在", nil)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// Optional image resizing (?w=480 thumbnail or ?w=2560 viewer).
	// GIFs, live photos, videos, and audio are always returned as original files (design §6).
	if rawW := strings.TrimSpace(r.URL.Query().Get("w")); rawW != "" && isResizableImageExt(relPath) {
		maxEdge, parseErr := strconv.Atoi(rawW)
		if parseErr != nil || maxEdge <= 0 {
			writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "缩放参数不正确", nil)
			return
		}
		if maxEdge > 4096 {
			maxEdge = 4096
		}
		if s.thumbs != nil {
			if jpegData, tErr := s.thumbs.GetOrBuild(itemKey, relPath, fullPath, maxEdge); tErr == nil {
				w.Header().Set("Content-Type", "image/jpeg")
				http.ServeContent(w, r, "thumb.jpg", time.Time{}, bytes.NewReader(jpegData))
				return
			}
		}
	}

	f, err := os.Open(fullPath)
	if err != nil {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "媒体文件不可读取", nil)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "媒体文件不可读取", nil)
		return
	}
	if ct := mediaContentType(relPath); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// http.ServeContent handles Range, If-Range, Content-Length, and HEAD automatically.
	http.ServeContent(w, r, filepath.Base(relPath), info.ModTime(), f)
}

func (s *Server) handleAvatar(w http.ResponseWriter, r *http.Request, rest string) {
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash >= len(rest)-1 {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "头像不存在", nil)
		return
	}
	siteID, err1 := url.PathUnescape(rest[:slash])
	uid, err2 := url.PathUnescape(rest[slash+1:])
	if err1 != nil || err2 != nil || strings.TrimSpace(siteID) == "" || strings.TrimSpace(uid) == "" {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "头像不存在", nil)
		return
	}
	if !site.ID(strings.ToLower(strings.TrimSpace(siteID))).Valid() {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "头像不存在", nil)
		return
	}
	avatarPath := s.backend.AccountAvatarPath(strings.ToLower(strings.TrimSpace(siteID)), strings.TrimSpace(uid))
	if strings.TrimSpace(avatarPath) == "" {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "头像不存在", nil)
		return
	}
	f, err := os.Open(avatarPath)
	if err != nil {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "头像不存在", nil)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "头像不存在", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "image/png")
	http.ServeContent(w, r, "avatar.png", info.ModTime(), f)
}

func mediaContentType(relPath string) string {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".vtt":
		return "text/vtt; charset=utf-8"
	case ".xml":
		return "application/xml; charset=utf-8"
	default:
		return ""
	}
}
