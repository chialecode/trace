package companion

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/service"
)

const (
	livePreviewStreamPollInterval = 150 * time.Millisecond
	livePreviewHLSWindowFrags     = 12
	livePreviewMaxScanBoxes       = 65536
)

type fmp4FragSpan struct {
	offset int64
	length int64
}

type fmp4ScanResult struct {
	initEnd      int64
	committedEnd int64
	frags        []fmp4FragSpan
	unsupported  bool
}

// scanLiveFMP4 scans complete top-level ISO BMFF boxes in f up to diskLimit
// (ftyp + moov init segment, followed by complete moof + mdat fragment pairs).
// Any incomplete trailing box is excluded so clients never receive truncated boxes.
func scanLiveFMP4(f *os.File, diskLimit int64) (fmp4ScanResult, error) {
	var res fmp4ScanResult
	if diskLimit < 16 {
		return res, nil
	}

	var off int64
	var seenFtyp bool
	var seenMoov bool
	var pendingMoofStart int64 = -1
	var hdr [16]byte

	for boxes := 0; boxes < livePreviewMaxScanBoxes && off+8 <= diskLimit; boxes++ {
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return res, err
		}
		// Reject raw FLV header ("FLV\x01") or obviously non-ISO-BMFF stream.
		if off == 0 && hdr[0] == 'F' && hdr[1] == 'L' && hdr[2] == 'V' {
			res.unsupported = true
			return res, nil
		}

		size32 := binary.BigEndian.Uint32(hdr[0:4])
		boxType := string(hdr[4:8])
		var boxSize int64
		var hdrLen int64 = 8
		switch size32 {
		case 0:
			// Box extends to EOF: cannot be considered complete in a growing .part file.
			return res, nil
		case 1:
			if off+16 > diskLimit {
				return res, nil
			}
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					return res, nil
				}
				return res, err
			}
			sz64 := binary.BigEndian.Uint64(hdr[8:16])
			if sz64 < 16 || sz64 > math.MaxInt64 {
				res.unsupported = true
				return res, nil
			}
			boxSize = int64(sz64)
			hdrLen = 16
		default:
			if size32 < 8 {
				res.unsupported = true
				return res, nil
			}
			boxSize = int64(size32)
		}

		if boxSize < hdrLen || off+boxSize > diskLimit {
			// Incomplete box still being flushed by recorder.
			break
		}
		if off == 0 && !isKnownInitBox(boxType) {
			res.unsupported = true
			return res, nil
		}

		nextOff := off + boxSize
		if !seenMoov {
			switch boxType {
			case "ftyp":
				seenFtyp = true
			case "moov":
				seenMoov = true
				res.initEnd = nextOff
				res.committedEnd = nextOff
			case "free", "skip", "wide", "pdin", "styp", "sidx", "meta":
				// Benign top-level container boxes before moov.
			default:
				if !seenFtyp {
					res.unsupported = true
					return res, nil
				}
			}
		} else {
			switch boxType {
			case "moof":
				pendingMoofStart = off
			case "mdat":
				if pendingMoofStart >= res.initEnd {
					res.frags = append(res.frags, fmp4FragSpan{
						offset: pendingMoofStart,
						length: nextOff - pendingMoofStart,
					})
					res.committedEnd = nextOff
					pendingMoofStart = -1
				}
			case "styp", "sidx", "prft", "emsg", "free", "skip", "mfra":
				// Optional segment/index boxes between fragments.
				if pendingMoofStart < 0 {
					res.committedEnd = nextOff
				}
			default:
				pendingMoofStart = -1
			}
		}
		off = nextOff
	}
	return res, nil
}

func isKnownInitBox(boxType string) bool {
	switch boxType {
	case "ftyp", "styp", "moov", "free", "skip", "wide", "pdin":
		return true
	default:
		return false
	}
}

func safeLivePartPath(info service.LiveRecordingPreviewInfo) (string, bool) {
	partPath := strings.TrimSpace(info.PartPath)
	if partPath == "" {
		return "", false
	}
	cleanPart := filepath.Clean(partPath)
	if !strings.HasSuffix(strings.ToLower(cleanPart), ".part") {
		return "", false
	}
	if dir := strings.TrimSpace(info.ItemDir); dir != "" {
		cleanDir := filepath.Clean(dir)
		rel, err := filepath.Rel(cleanDir, cleanPart)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return "", false
		}
	}
	return cleanPart, true
}

func safeLiveDiskLimit(f *os.File, info service.LiveRecordingPreviewInfo) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !st.Mode().IsRegular() {
		return 0, os.ErrInvalid
	}
	limit := st.Size()
	if info.PartBytes > 0 && info.PartBytes < limit {
		limit = info.PartBytes
	}
	return limit, nil
}

func (s *Server) lookupLivePreview(w http.ResponseWriter, r *http.Request, recKey string) (service.LiveRecordingPreviewInfo, string, bool) {
	info, ok := s.backend.LiveRecordingPreview(recKey)
	if !ok {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "找不到对应的直播录制", nil)
		return service.LiveRecordingPreviewInfo{}, "", false
	}
	if !info.Recording {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "直播录制已结束，请在媒体库中查看回放", nil)
		return service.LiveRecordingPreviewInfo{}, "", false
	}
	if !info.PreviewSupported || info.AudioOnly {
		writeError(w, http.StatusBadRequest, companionapi.CodeUnsupported, firstNonEmpty(info.PreviewReason, "这场录制暂不支持实时预览"), nil)
		return service.LiveRecordingPreviewInfo{}, "", false
	}
	if rawPart := strings.TrimSpace(r.URL.Query().Get("part")); rawPart != "" {
		wantPart, err := strconv.Atoi(rawPart)
		if err != nil || wantPart <= 0 {
			writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "分段编号不正确", nil)
			return service.LiveRecordingPreviewInfo{}, "", false
		}
		if info.PartIndex > 0 && wantPart != info.PartIndex {
			writeError(w, http.StatusConflict, companionapi.CodeConflict, "录制已切换到新分段，请重新加载预览", nil)
			return service.LiveRecordingPreviewInfo{}, "", false
		}
	}
	partPath, pathOK := safeLivePartPath(info)
	if !info.PartReady || !pathOK {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, companionapi.CodeUnavailable, firstNonEmpty(info.PreviewReason, "等待首个直播分片写入"), nil)
		return service.LiveRecordingPreviewInfo{}, "", false
	}
	return info, partPath, true
}

func (s *Server) handleLiveRecordingPreview(w http.ResponseWriter, r *http.Request, recKey string) {
	if !s.requireAnyLogin(w) {
		return
	}
	info, partPath, ok := s.lookupLivePreview(w, r, recKey)
	if !ok {
		return
	}

	scan, err := scanLivePartSnapshot(partPath, info)
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, companionapi.CodeUnavailable, "录制分段暂不可读取，请稍后重试", nil)
		return
	}
	if scan.unsupported {
		writeError(w, http.StatusBadRequest, companionapi.CodeUnsupported, "这场录制格式暂不支持实时预览", nil)
		return
	}
	if scan.initEnd == 0 || scan.committedEnd <= scan.initEnd {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, companionapi.CodeUnavailable, firstNonEmpty(info.PreviewReason, "等待首个直播分片写入"), nil)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "video/mp4")
	if info.PartIndex > 0 {
		w.Header().Set(companionapi.HeaderLivePart, strconv.Itoa(info.PartIndex))
	}
	w.Header().Set(companionapi.HeaderLiveBytes, strconv.FormatInt(scan.committedEnd, 10))

	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("mode")), "stream") && r.Method == http.MethodGet {
		s.streamLivePartContinuous(w, r, recKey, info, partPath, scan.committedEnd)
		return
	}

	// Default Range-capable read-only snapshot over complete fMP4 boxes.
	// Open-read-close per HTTP request so zero file handles stay held between reads.
	f, err := openSharedReadFile(partPath)
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, companionapi.CodeUnavailable, "录制分段暂不可读取，请稍后重试", nil)
		return
	}
	defer f.Close()

	w.Header().Set("Accept-Ranges", "bytes")
	sr := io.NewSectionReader(f, 0, scan.committedEnd)
	name := fmt.Sprintf("live-part-%d.mp4", info.PartIndex)
	http.ServeContent(w, r, name, time.Time{}, sr)
}

func scanLivePartSnapshot(partPath string, info service.LiveRecordingPreviewInfo) (fmp4ScanResult, error) {
	f, err := openSharedReadFile(partPath)
	if err != nil {
		return fmp4ScanResult{}, err
	}
	defer f.Close()
	limit, err := safeLiveDiskLimit(f, info)
	if err != nil {
		return fmp4ScanResult{}, err
	}
	return scanLiveFMP4(f, limit)
}

// streamLivePartContinuous streams complete fMP4 fragments continuously as they are
// committed to the active .part file. Between polls the file handle is closed so
// Windows finalization (os.Rename / os.Remove) is never blocked, and client disconnect,
// recording stop, or part rotation terminates the stream immediately.
func (s *Server) streamLivePartContinuous(
	w http.ResponseWriter,
	r *http.Request,
	recKey string,
	initial service.LiveRecordingPreviewInfo,
	partPath string,
	initialEnd int64,
) {
	flusher, _ := w.(http.Flusher)
	w.WriteHeader(http.StatusOK)

	var sent int64
	copyChunk := func(end int64) bool {
		if end <= sent {
			return true
		}
		f, err := openSharedReadFile(partPath)
		if err != nil {
			return false
		}
		n, copyErr := io.Copy(w, io.NewSectionReader(f, sent, end-sent))
		_ = f.Close()
		sent += n
		if flusher != nil && n > 0 {
			flusher.Flush()
		}
		return copyErr == nil
	}

	if !copyChunk(initialEnd) {
		return
	}

	ticker := time.NewTicker(livePreviewStreamPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			cur, ok := s.backend.LiveRecordingPreview(recKey)
			if !ok || !cur.Recording || cur.PartIndex != initial.PartIndex || filepath.Clean(cur.PartPath) != partPath {
				return
			}
			scan, err := scanLivePartSnapshot(partPath, cur)
			if err != nil || scan.unsupported {
				return
			}
			if scan.committedEnd > sent {
				if !copyChunk(scan.committedEnd) {
					return
				}
			}
		}
	}
}

// handleLiveRecordingPreviewHLS serves an in-memory RFC 8216 v7 live fMP4 HLS playlist
// referencing byte-ranges of GET /api/v1/live/recordings/{id}/preview?part=N.
// Zero extra disk files, zero extra site requests, zero lingering file handles.
func (s *Server) handleLiveRecordingPreviewHLS(w http.ResponseWriter, r *http.Request, recKey string) {
	if !s.requireAnyLogin(w) {
		return
	}
	info, partPath, ok := s.lookupLivePreview(w, r, recKey)
	if !ok {
		return
	}
	scan, err := scanLivePartSnapshot(partPath, info)
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, companionapi.CodeUnavailable, "录制分段暂不可读取，请稍后重试", nil)
		return
	}
	if scan.unsupported {
		writeError(w, http.StatusBadRequest, companionapi.CodeUnsupported, "这场录制格式暂不支持实时预览", nil)
		return
	}
	if scan.initEnd == 0 || len(scan.frags) == 0 {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, companionapi.CodeUnavailable, firstNonEmpty(info.PreviewReason, "等待首个直播分片写入"), nil)
		return
	}

	startIdx := 0
	if len(scan.frags) > livePreviewHLSWindowFrags {
		startIdx = len(scan.frags) - livePreviewHLSWindowFrags
	}
	fragDur := 2.0
	if info.PartDurationSec > 0 && len(scan.frags) > 0 {
		fragDur = info.PartDurationSec / float64(len(scan.frags))
		if fragDur < 0.5 {
			fragDur = 0.5
		} else if fragDur > 6.0 {
			fragDur = 6.0
		}
	}
	targetDur := int(math.Ceil(fragDur))
	if targetDur < 4 {
		targetDur = 4
	}

	partURI := "preview"
	if info.PartIndex > 0 {
		partURI = fmt.Sprintf("preview?part=%d", info.PartIndex)
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDur)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", startIdx)
	fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q,BYTERANGE=\"%d@0\"\n", partURI, scan.initEnd)
	for _, frag := range scan.frags[startIdx:] {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n", fragDur)
		fmt.Fprintf(&b, "#EXT-X-BYTERANGE:%d@%d\n", frag.length, frag.offset)
		b.WriteString(partURI + "\n")
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	if info.PartIndex > 0 {
		w.Header().Set(companionapi.HeaderLivePart, strconv.Itoa(info.PartIndex))
	}
	w.Header().Set(companionapi.HeaderLiveBytes, strconv.FormatInt(scan.committedEnd, 10))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, b.String())
	}
}

// matchLiveRecordingPreviewRoute parses "live/recordings/{id}/preview" and
// "live/recordings/{id}/preview.m3u8".
func matchLiveRecordingPreviewRoute(rel string) (recKey string, isHLS bool, ok bool) {
	if !strings.HasPrefix(rel, "live/recordings/") {
		return "", false, false
	}
	rest := strings.TrimPrefix(rel, "live/recordings/")
	switch {
	case strings.HasSuffix(rest, "/preview.m3u8"):
		rawID := strings.TrimSuffix(rest, "/preview.m3u8")
		id, err := url.PathUnescape(rawID)
		if err != nil || strings.TrimSpace(id) == "" {
			return "", true, true // matched route, invalid ID -> caller handles 400
		}
		return strings.TrimSpace(id), true, true
	case strings.HasSuffix(rest, "/preview"):
		rawID := strings.TrimSuffix(rest, "/preview")
		id, err := url.PathUnescape(rawID)
		if err != nil || strings.TrimSpace(id) == "" {
			return "", false, true
		}
		return strings.TrimSpace(id), false, true
	default:
		return "", false, false
	}
}
