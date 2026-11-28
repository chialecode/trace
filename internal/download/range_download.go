package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	jitterrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// networkPauseError signals that segmented range retries or full resets reached
// their upper bound and the task should pause with reason "network" if the
// caller does not recover via a fallback stream. Returning this typed error
// instead of mutating Task.Status mid-task prevents SQLite status poisoning
// when a caller recovers (e.g. Douyin OriginalURLs falling back to VideoURLs
// or a non-fatal cover/page failure).
type networkPauseError struct {
	message string
	err     error
}

func (e *networkPauseError) Error() string {
	if e.message != "" {
		return e.message
	}
	if e.err != nil {
		return e.err.Error()
	}
	return "已暂停（网络）"
}

func (e *networkPauseError) Unwrap() error {
	return e.err
}

type mediaTransportKey struct {
	site           site.ID
	allowLocalhost bool
}

type segmentResult struct {
	newOffset       int64
	totalSize       int64
	done            bool
	truncatedToZero bool
}

type rangeDownloadSpec struct {
	site           site.ID
	role           string
	validateURL    func(string) error
	applyHeaders   func(*http.Request)
	doRequest      func(*http.Request) (*http.Response, error)
	wrapBody       func(io.ReadCloser) io.ReadCloser
	checkDisguised bool
	reuseVerified  bool
	updateFinalRow bool
	allRejectedMsg string
}

func (m *Manager) bilibiliRangeSpec() rangeDownloadSpec {
	mediaClient, _ := m.client.MediaHTTPClient()
	return rangeDownloadSpec{
		site: site.Bilibili,
		validateURL: func(raw string) error {
			if m.client != nil {
				return m.client.ValidateMediaURL(raw)
			}
			return nil
		},
		applyHeaders: func(req *http.Request) {
			req.Header.Set("User-Agent", bilibili.DefaultUserAgent)
			req.Header.Set("Referer", bilibili.DefaultReferer)
		},
		doRequest: mediaClient.Do,
		wrapBody: func(rc io.ReadCloser) io.ReadCloser {
			if m.client != nil {
				return m.client.WrapMediaReader(rc)
			}
			return rc
		},
		reuseVerified:  true,
		updateFinalRow: true,
		allRejectedMsg: "站点只返回了不受支持的第三方媒体节点，请稍后重试",
	}
}

func (m *Manager) transportRangeSpec(transport site.MediaTransportSpec, role string, reuseVerified bool) rangeDownloadSpec {
	httpClient := m.transportHTTPClient(transport)
	return rangeDownloadSpec{
		site:         transport.Site,
		role:         role,
		validateURL:  transport.ValidateURL,
		applyHeaders: transport.ApplyHeaders,
		doRequest:    httpClient.Do,
		wrapBody: func(rc io.ReadCloser) io.ReadCloser {
			return site.WrapIdleReadCloser(rc, 20*time.Second, func() error {
				return errors.New("media read idle timeout")
			})
		},
		checkDisguised: true,
		reuseVerified:  reuseVerified,
	}
}

func (m *Manager) downloadStreamWithRefresh(
	ctx context.Context,
	urls []string,
	finalPath string,
	t *Task,
	refreshURLs func() ([]string, error),
) (int64, string, error) {
	n, sha, _, err := m.downloadRangeWithRefresh(ctx, m.bilibiliRangeSpec(), urls, finalPath, t, refreshURLs)
	return n, sha, err
}

// downloadFileWithRange downloads from candidate URLs into finalPath via <finalPath>.part,
// using 8 MiB closed-interval Range requests, per-segment retry with exponential backoff + URL rotation,
// and Content-Range verification (data-and-cache.md §2.4).
func (m *Manager) downloadFileWithRange(ctx context.Context, urls []string, finalPath string, t *Task) (int64, string, error) {
	n, sha, _, err := m.downloadRange(ctx, m.bilibiliRangeSpec(), urls, finalPath, t)
	return n, sha, err
}

func (m *Manager) fetchSingleSegment(
	ctx context.Context,
	targetURL string,
	partPath string,
	offset int64,
	endByte int64,
	knownTotal int64,
	t *Task,
) (segmentResult, error) {
	relFile := taskRelativePath(t, strings.TrimSuffix(partPath, ".part"))
	relID := taskFileIdentity(relFile)
	res, _, err := m.fetchRangeSegment(ctx, m.bilibiliRangeSpec(), targetURL, partPath, relID, "", offset, endByte, knownTotal, t)
	return res, err
}

func (m *Manager) downloadWithTransportRefresh(
	ctx context.Context,
	transport site.MediaTransportSpec,
	urls []string,
	finalPath string,
	role string,
	t *Task,
	refreshURLs func() ([]string, error),
) (int64, string, error) {
	n, sha, _, err := m.downloadRangeWithRefresh(ctx, m.transportRangeSpec(transport, role, true), urls, finalPath, t, refreshURLs)
	return n, sha, err
}

func (m *Manager) downloadWithTransportRefreshCT(
	ctx context.Context,
	transport site.MediaTransportSpec,
	urls []string,
	finalPath string,
	role string,
	t *Task,
	refreshURLs func() ([]string, error),
) (int64, string, string, error) {
	return m.downloadRangeWithRefresh(ctx, m.transportRangeSpec(transport, role, false), urls, finalPath, t, refreshURLs)
}

func (m *Manager) downloadRangeWithRefresh(
	ctx context.Context,
	spec rangeDownloadSpec,
	urls []string,
	finalPath string,
	t *Task,
	refreshURLs func() ([]string, error),
) (int64, string, string, error) {
	n, sha, ct, err := m.downloadRange(ctx, spec, urls, finalPath, t)
	if err == nil {
		return n, sha, ct, nil
	}
	// If all URLs expired (403, 404, 410), refresh at most once and resume from byte offset (data-and-cache.md §2.4).
	if refreshURLs != nil && (site.IsKind(err, site.ErrKindForbidden) || site.IsKind(err, site.ErrKindUnavailable)) {
		freshURLs, rErr := refreshURLs()
		if rErr != nil {
			if ctx.Err() != nil {
				return 0, "", "", ctx.Err()
			}
			if isSiteStopError(rErr) || isStorageError(rErr) || site.IsKind(rErr, site.ErrKindUnavailable) {
				return 0, "", "", rErr
			}
			return 0, "", "", err
		}
		if len(freshURLs) > 0 {
			return m.downloadRange(ctx, spec, freshURLs, finalPath, t)
		}
	}
	return 0, "", "", err
}

func (m *Manager) downloadRange(
	ctx context.Context,
	spec rangeDownloadSpec,
	urls []string,
	finalPath string,
	t *Task,
) (int64, string, string, error) {
	var cleanURLs []string
	var lastValidateErr error
	rejectedNodes := 0
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if spec.validateURL != nil {
			if vErr := spec.validateURL(u); vErr != nil {
				lastValidateErr = vErr
				rejectedNodes++
				continue
			}
		}
		cleanURLs = append(cleanURLs, u)
	}
	if len(cleanURLs) == 0 {
		if rejectedNodes > 0 {
			if spec.allRejectedMsg != "" {
				return 0, "", "", &site.Error{
					Site:    spec.site,
					Kind:    site.ErrKindUnavailable,
					Message: spec.allRejectedMsg,
				}
			}
			if lastValidateErr != nil {
				return 0, "", "", lastValidateErr
			}
		}
		return 0, "", "", &site.Error{
			Site:    spec.site,
			Kind:    site.ErrKindUnavailable,
			Message: "下载地址列表为空",
		}
	}

	root, rel := filepath.Dir(finalPath), filepath.Base(finalPath)
	if t.ItemDir != "" {
		root = t.ItemDir
		r, err := filepath.Rel(root, finalPath)
		if err != nil {
			return 0, "", "", err
		}
		rel = filepath.ToSlash(r)
	}
	if _, err := library.ResolveItemPath(root, rel); err != nil {
		return 0, "", "", err
	}
	if _, err := library.ResolveItemPath(root, rel+".part"); err != nil {
		return 0, "", "", err
	}
	if err := m.claimTaskFile(t, root, rel, finalPath); err != nil {
		return 0, "", "", err
	}
	if err := ctx.Err(); err != nil {
		return 0, "", "", err
	}

	relID := taskFileIdentity(rel)
	trimmedRel := taskRelativePath(t, trimStagingSuffixes(finalPath))
	trimmedID := taskFileIdentity(trimmedRel)

	// A nonempty file alone is not proof of completion. Reuse only a verified
	// checkpoint with its original length (and hash, when available).
	if spec.reuseVerified {
		for _, file := range t.Files {
			if taskFileIdentity(file.Path) == relID && file.TotalBytes > 0 && library.VerifyMediaFile(root, library.MediaFile{Path: rel, Bytes: file.TotalBytes, SHA256: file.SHA256}, true) {
				sha, sz, err := hashFileSHA256(finalPath)
				return sz, sha, "", err
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return 0, "", "", err
	}

	m.mu.Lock()
	chunkSize := m.chunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultRangeChunkSize
	}
	backoffScale := m.backoffScale
	if backoffScale <= 0 {
		backoffScale = time.Second
	}
	m.mu.Unlock()

	partPath := finalPath + ".part"
	attemptFile := filepath.Base(finalPath)
	if spec.checkDisguised {
		attemptFile = trimmedRel
	}

	var offset int64
	if info, err := os.Stat(partPath); err == nil && !info.IsDir() {
		offset = info.Size()
	}

	// Only constrain knownTotal from persisted progress when resuming an existing
	// non-empty .part file on disk. A fresh download (offset == 0) must adopt the
	// server's authoritative total so fallback streams (e.g. Douyin OriginalURLs
	// falling back to transcoded VideoURLs) or pre-poisoned tasks auto-heal.
	var totalSize int64 = -1
	if offset > 0 {
		for _, file := range t.Files {
			if taskFileIdentity(file.Path) == relID && file.TotalBytes > 0 {
				totalSize = file.TotalBytes
				break
			}
		}
		if totalSize < 0 && trimmedID != "" && trimmedID != relID {
			for _, file := range t.Files {
				if taskFileIdentity(file.Path) == trimmedID && file.TotalBytes > 0 {
					totalSize = file.TotalBytes
					break
				}
			}
		}
	}
	if totalSize > 0 && offset > totalSize {
		if err := os.Truncate(partPath, 0); err != nil {
			return 0, "", "", err
		}
		offset = 0
		totalSize = -1
	}

	var contentType string
	urlIdx := 0
	segRetries := 0
	fullResets := 0
	var lastErr error

	for {
		if ctx.Err() != nil {
			return offset, "", "", ctx.Err()
		}
		if totalSize > 0 && offset == totalSize {
			break
		}

		targetURL := cleanURLs[urlIdx%len(cleanURLs)]
		endByte := offset + chunkSize - 1
		if totalSize > 0 && endByte >= totalSize {
			endByte = totalSize - 1
		}

		res, ct, err := m.fetchRangeSegment(ctx, spec, targetURL, partPath, relID, trimmedID, offset, endByte, totalSize, t)
		if err == nil {
			if ct != "" {
				contentType = ct
			}
			offset = res.newOffset
			if res.totalSize > 0 {
				totalSize = res.totalSize
				t.TotalBytes = totalSize
			}
			for i := range t.Files {
				fid := taskFileIdentity(t.Files[i].Path)
				if fid == relID || (trimmedID != "" && fid == trimmedID) {
					t.Files[i].DownloadedBytes = offset
					if totalSize > 0 {
						t.Files[i].TotalBytes = totalSize
					}
				}
			}
			segRetries = 0
			if res.done || (totalSize > 0 && offset >= totalSize) {
				break
			}
			continue
		}

		lastErr = err
		errKind := classifyDownloadError(err)
		httpStatus := 0
		var se *site.Error
		if errors.As(err, &se) {
			httpStatus = se.HTTPStatus
		}

		// Risk control (412 / -352 / -412), rate limit (429), auth required (401), blocked, schema changed, or storage: stop immediately without retry.
		if errKind == site.ErrKindRiskControl || errKind == site.ErrKindRateLimited || errKind == site.ErrKindAuthRequired || errKind == site.ErrKindBlocked || errKind == site.ErrKindSchemaChanged || isStorageError(err) {
			m.recordAttempt(t, attemptFile, offset, targetURL, segRetries+1, errKind, httpStatus)
			return offset, "", "", err
		}

		// Subsequent segment returned 200 OK instead of 206 or 416: truncate .part, rotate URL, and count towards 5-reset limit.
		if res.truncatedToZero {
			offset = 0
			totalSize = -1
			fullResets++
			urlIdx++
			m.recordAttempt(t, attemptFile, offset, targetURL, fullResets, site.ErrKindNetwork, http.StatusOK)
			if fullResets >= 5 {
				return offset, "", "", &networkPauseError{
					message: "已暂停（网络）：节点不支持分段续传且重试已达上限",
					err:     lastErr,
				}
			}
			continue
		}

		// Expired/forbidden URL (403, 404, 410): rotate through remaining candidate URLs once without sleeping.
		if errKind == site.ErrKindForbidden || errKind == site.ErrKindUnavailable {
			segRetries++
			urlIdx++
			m.recordAttempt(t, attemptFile, offset, targetURL, segRetries, errKind, httpStatus)
			if segRetries >= len(cleanURLs) {
				return offset, "", "", lastErr
			}
			continue
		}

		// Transport error (premature EOF, timeout, reset, 5xx): keep written bytes, retry up to 5 times with exponential backoff and URL rotation.
		offset = res.newOffset
		if res.totalSize > 0 {
			totalSize = res.totalSize
		}
		segRetries++
		urlIdx++
		m.recordAttempt(t, attemptFile, offset, targetURL, segRetries, errKind, httpStatus)
		if segRetries >= 5 {
			return offset, "", "", &networkPauseError{
				message: "已暂停（网络）：分段重试已达上限",
				err:     lastErr,
			}
		}

		// Exponential backoff: 1, 2, 4, 8, 16 units
		backoffUnits := int64(1 << (segRetries - 1))
		if backoffUnits > 16 {
			backoffUnits = 16
		}
		sleepDur := time.Duration(backoffUnits) * backoffScale
		sleepDur = time.Duration(float64(sleepDur) * (0.8 + 0.4*jitterrand.Float64()))
		select {
		case <-ctx.Done():
			return offset, "", "", ctx.Err()
		case <-time.After(sleepDur):
		}
	}

	shaHex, actualSize, err := hashFileSHA256(partPath)
	if err != nil {
		return offset, "", "", err
	}
	if totalSize > 0 && actualSize != totalSize {
		return actualSize, "", "", &site.Error{
			Site:    spec.site,
			Kind:    site.ErrKindNetwork,
			Message: fmt.Sprintf("文件大小校验不一致: 实际 %d != 预期 %d", actualSize, totalSize),
		}
	}

	if err := os.Rename(partPath, finalPath); err != nil {
		return actualSize, "", "", err
	}
	for i := range t.Files {
		if taskFileIdentity(t.Files[i].Path) == relID {
			t.Files[i].DownloadedBytes, t.Files[i].TotalBytes = actualSize, actualSize
			t.Files[i].SHA256 = shaHex
			if spec.updateFinalRow {
				t.Files[i].Status = "completed"
			}
		}
	}
	if spec.updateFinalRow || spec.reuseVerified {
		m.persistTaskProgress(*t)
	}
	return actualSize, shaHex, contentType, nil
}

func (m *Manager) fetchRangeSegment(
	ctx context.Context,
	spec rangeDownloadSpec,
	targetURL string,
	partPath string,
	relID string,
	trimmedID string,
	offset int64,
	endByte int64,
	knownTotal int64,
	t *Task,
) (segmentResult, string, error) {
	if spec.validateURL != nil {
		if err := spec.validateURL(targetURL); err != nil {
			return segmentResult{newOffset: offset}, "", err
		}
	}
	safeEndpoint := site.SanitizeEndpoint(targetURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return segmentResult{newOffset: offset}, "", &site.Error{
			Site:     spec.site,
			Kind:     site.ErrKindBadRequest,
			Endpoint: safeEndpoint,
			Message:  "媒体请求构造失败",
			Err:      site.UnwrapURLError(err),
		}
	}
	if spec.applyHeaders != nil {
		spec.applyHeaders(req)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, endByte))

	resp, err := spec.doRequest(req)
	if err != nil {
		var se *site.Error
		if errors.As(err, &se) {
			return segmentResult{newOffset: offset}, "", se
		}
		return segmentResult{newOffset: offset}, "", &site.Error{
			Site:     spec.site,
			Kind:     site.ErrKindNetwork,
			Endpoint: safeEndpoint,
			Message:  "媒体下载连接失败",
			Err:      site.UnwrapURLError(err),
		}
	}
	defer resp.Body.Close()

	// Handle 416 Range Not Satisfiable when offset > 0 by truncating .part and restarting.
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0 {
		_ = os.Truncate(partPath, 0)
		return segmentResult{newOffset: 0, truncatedToZero: true}, "", &site.Error{
			Site:       spec.site,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: resp.StatusCode,
			Endpoint:   safeEndpoint,
			Message:    "Range 416，重置分段文件",
		}
	}
	if resp.StatusCode >= 400 {
		return segmentResult{newOffset: offset}, "", mapMediaHTTPStatus(spec.site, resp.StatusCode, safeEndpoint)
	}

	rawCT := strings.TrimSpace(resp.Header.Get("Content-Type"))
	role := spec.role
	if role == "" {
		for _, file := range t.Files {
			fid := taskFileIdentity(file.Path)
			if (fid == relID || (trimmedID != "" && fid == trimmedID)) && file.Role != "" {
				role = file.Role
				break
			}
		}
	}
	isImage := role == "image" || role == "cover" || role == "image_nowm"
	if spec.checkDisguised {
		if err := rejectDisguisedMediaContentType(spec.site, rawCT, role, resp.StatusCode); err != nil {
			return segmentResult{newOffset: offset}, rawCT, err
		}
	} else if isImage && !strings.HasPrefix(strings.ToLower(rawCT), "image/") {
		return segmentResult{newOffset: offset}, rawCT, &site.Error{
			Site:    spec.site,
			Kind:    site.ErrKindSchemaChanged,
			Message: "图片响应不是 image/*",
		}
	}

	var expectedSegLen int64 = -1
	totalSize := knownTotal
	doneAfterThis := false
	openFlags := os.O_CREATE | os.O_WRONLY
	seekToOffset := false

	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := strings.TrimSpace(resp.Header.Get("Content-Range"))
		if cr == "" {
			return segmentResult{newOffset: offset}, rawCT, &site.Error{Site: spec.site, Kind: site.ErrKindNetwork, Message: "206 响应缺少 Content-Range"}
		}
		start, end, total, ok := parseContentRange(cr)
		if !ok || start != offset || end < start || end > endByte || total <= end || (knownTotal > 0 && total != knownTotal) {
			return segmentResult{newOffset: offset}, rawCT, &site.Error{
				Site:    spec.site,
				Kind:    site.ErrKindNetwork,
				Message: fmt.Sprintf("Content-Range 校验失败: %q (offset=%d, knownTotal=%d)", cr, offset, knownTotal),
			}
		}
		if total > 0 {
			totalSize = total
		}
		expectedSegLen = end - start + 1
		if resp.ContentLength >= 0 && resp.ContentLength != expectedSegLen {
			return segmentResult{newOffset: offset}, rawCT, &site.Error{Site: spec.site, Kind: site.ErrKindNetwork, Message: "分段 Content-Length 与区间长度不一致"}
		}
		if totalSize > 0 && end+1 >= totalSize {
			doneAfterThis = true
		}
		seekToOffset = true

	case http.StatusOK:
		if offset > 0 {
			// Subsequent segment returned 200 OK instead of 206: truncate .part, rotate node, and retry from 0 (data-and-cache.md §2.4).
			_ = os.Remove(partPath)
			return segmentResult{newOffset: 0, truncatedToZero: true}, rawCT, &site.Error{
				Site:       spec.site,
				Kind:       site.ErrKindNetwork,
				HTTPStatus: http.StatusOK,
				Message:    "节点未返回 206 Partial Content，已截断从头重试",
			}
		}
		if resp.ContentLength <= 0 {
			return segmentResult{newOffset: offset}, rawCT, &site.Error{Site: spec.site, Kind: site.ErrKindNetwork, Message: "完整响应缺少有效的 Content-Length"}
		}
		expectedSegLen = resp.ContentLength
		totalSize = resp.ContentLength
		doneAfterThis = true
		openFlags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC

	default:
		return segmentResult{newOffset: offset}, rawCT, mapMediaHTTPStatus(spec.site, resp.StatusCode, safeEndpoint)
	}

	// Reject oversized images before opening or truncating .part on disk.
	if isImage && totalSize > MaxImageFileBytes {
		return segmentResult{newOffset: offset}, rawCT, &site.Error{
			Site:    spec.site,
			Kind:    site.ErrKindSchemaChanged,
			Message: "图片超过 128 MiB",
		}
	}

	f, err := os.OpenFile(partPath, openFlags, 0o600)
	if err != nil {
		return segmentResult{newOffset: offset}, rawCT, err
	}
	if seekToOffset {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return segmentResult{newOffset: offset}, rawCT, err
		}
	}

	wrapped := resp.Body
	if spec.wrapBody != nil {
		wrapped = spec.wrapBody(resp.Body)
	}
	defer wrapped.Close()
	reader := io.LimitReader(wrapped, expectedSegLen+1)

	buf := make([]byte, 64*1024)
	var segWritten int64
	written := offset
	checkedHead := !spec.checkDisguised || offset > 0
	if totalSize > 0 {
		t.TotalBytes = totalSize
	} else if resp.ContentLength > 0 {
		t.TotalBytes = offset + resp.ContentLength
	}

	progressAt := time.Now()
	progressBytes := offset
	for {
		nr, rErr := reader.Read(buf)
		if nr > 0 {
			if !checkedHead {
				checkedHead = true
				if looksLikeDisguisedTextBody(buf[:nr]) {
					_ = f.Close()
					_ = os.Remove(partPath)
					return segmentResult{newOffset: 0}, rawCT, &site.Error{
						Site:       spec.site,
						Kind:       site.ErrKindSchemaChanged,
						HTTPStatus: resp.StatusCode,
						Message:    "媒体响应正文为 HTML 或 JSON 文本，已拒绝写入",
					}
				}
			}
			if segWritten+int64(nr) > expectedSegLen {
				_ = f.Close()
				if tErr := os.Truncate(partPath, offset); tErr != nil {
					return segmentResult{newOffset: offset}, rawCT, tErr
				}
				return segmentResult{newOffset: offset, totalSize: totalSize}, rawCT, &site.Error{Site: spec.site, Kind: site.ErrKindNetwork, Message: "分段正文超出声明区间"}
			}
			nw, wErr := f.Write(buf[:nr])
			if wErr != nil {
				_ = f.Close()
				return segmentResult{newOffset: written, totalSize: totalSize}, rawCT, wErr
			}
			segWritten += int64(nw)
			written += int64(nw)
			t.BytesDownloaded = written
			if t.TotalBytes > 0 {
				t.Progress = float64(written) / float64(t.TotalBytes)
				if t.Progress > 0.99 {
					t.Progress = 0.99
				}
			}
		}
		if elapsed := time.Since(progressAt); elapsed >= time.Second {
			speed := int64(float64(written-progressBytes) / elapsed.Seconds())
			for i := range t.Files {
				fid := taskFileIdentity(t.Files[i].Path)
				if fid == relID || (trimmedID != "" && fid == trimmedID) {
					t.Files[i].DownloadedBytes = written
					t.Files[i].TotalBytes = t.TotalBytes
					t.Files[i].SpeedBps = speed
					t.Files[i].Status = "downloading"
				}
			}
			m.persistTaskProgress(*t)
			progressAt = time.Now()
			progressBytes = written
		}

		if rErr != nil {
			if rErr == io.EOF {
				break
			}
			_ = f.Sync()
			_ = f.Close()
			return segmentResult{newOffset: written, totalSize: totalSize}, rawCT, &site.Error{
				Site:     spec.site,
				Kind:     site.ErrKindNetwork,
				Endpoint: safeEndpoint,
				Message:  "媒体分段读取中断",
				Err:      site.UnwrapURLError(rErr),
			}
		}
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		return segmentResult{newOffset: written, totalSize: totalSize}, rawCT, err
	}
	if err := f.Close(); err != nil {
		return segmentResult{newOffset: written, totalSize: totalSize}, rawCT, err
	}

	if expectedSegLen >= 0 && segWritten != expectedSegLen {
		return segmentResult{newOffset: written, totalSize: totalSize}, rawCT, &site.Error{
			Site:     spec.site,
			Kind:     site.ErrKindNetwork,
			Endpoint: safeEndpoint,
			Message:  fmt.Sprintf("分段字节未读满: 实读 %d < 预期 %d", segWritten, expectedSegLen),
		}
	}

	return segmentResult{
		newOffset: written,
		totalSize: totalSize,
		done:      doneAfterThis,
	}, rawCT, nil
}

func newGuardedMediaTransport(siteID site.ID, allowLocalhost bool) *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	guard := site.NewGuardedMediaDialer(siteID, allowLocalhost, dialer, "已拦截内网或回环媒体节点地址")
	return &http.Transport{
		Proxy:                 guard.Proxy,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext:           guard.DialContext,
	}
}

func makeMediaRedirectCheck(spec site.MediaTransportSpec) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return &site.Error{
				Site:    spec.Site,
				Kind:    site.ErrKindBlocked,
				Message: "媒体重定向超过 5 跳上限",
			}
		}
		if err := spec.ValidateRedirectURL(req.URL.String()); err != nil {
			return err
		}
		spec.ApplyHeaders(req)
		return nil
	}
}

func newTransportHTTPClient(spec site.MediaTransportSpec) *http.Client {
	return &http.Client{
		Transport:     newGuardedMediaTransport(spec.Site, spec.AllowLocalhost),
		CheckRedirect: makeMediaRedirectCheck(spec),
	}
}

func (m *Manager) transportHTTPClient(spec site.MediaTransportSpec) *http.Client {
	key := mediaTransportKey{site: spec.Site, allowLocalhost: spec.AllowLocalhost}
	m.mu.Lock()
	if m.transports == nil {
		m.transports = make(map[mediaTransportKey]*http.Transport)
	}
	tr, ok := m.transports[key]
	if !ok {
		tr = newGuardedMediaTransport(spec.Site, spec.AllowLocalhost)
		m.transports[key] = tr
	}
	mediaWrap := m.mediaWrap
	m.mu.Unlock()

	var rt http.RoundTripper = tr
	if mediaWrap != nil {
		rt = mediaWrap(tr)
	}
	return &http.Client{
		Transport:     rt,
		CheckRedirect: makeMediaRedirectCheck(spec),
	}
}

func parseContentRange(cr string) (start, end, total int64, ok bool) {
	cr = strings.TrimSpace(cr)
	if !strings.HasPrefix(strings.ToLower(cr), "bytes ") {
		return 0, 0, 0, false
	}
	rest := strings.TrimSpace(cr[6:])
	slashIdx := strings.IndexByte(rest, '/')
	if slashIdx <= 0 {
		return 0, 0, 0, false
	}
	rangePart := rest[:slashIdx]
	totalPart := rest[slashIdx+1:]
	dashIdx := strings.IndexByte(rangePart, '-')
	if dashIdx <= 0 {
		return 0, 0, 0, false
	}
	sVal, err1 := strconv.ParseInt(strings.TrimSpace(rangePart[:dashIdx]), 10, 64)
	eVal, err2 := strconv.ParseInt(strings.TrimSpace(rangePart[dashIdx+1:]), 10, 64)
	if err1 != nil || err2 != nil || sVal < 0 || eVal < sVal {
		return 0, 0, 0, false
	}
	var tVal int64 = -1
	if totalPart != "*" {
		parsedTotal, err3 := strconv.ParseInt(strings.TrimSpace(totalPart), 10, 64)
		if err3 != nil || parsedTotal <= 0 {
			return 0, 0, 0, false
		}
		tVal = parsedTotal
	}
	return sVal, eVal, tVal, true
}

func mapMediaHTTPStatus(siteID site.ID, status int, endpoint string) *site.Error {
	endpoint = site.SanitizeEndpoint(endpoint)
	switch status {
	case 401:
		return &site.Error{Site: siteID, Kind: site.ErrKindAuthRequired, HTTPStatus: status, Endpoint: endpoint, Message: "媒体需要登录凭证 (401)"}
	case 403:
		return &site.Error{Site: siteID, Kind: site.ErrKindForbidden, HTTPStatus: status, Endpoint: endpoint, Message: "媒体访问被拒绝 (403)"}
	case 404, 410:
		return &site.Error{Site: siteID, Kind: site.ErrKindUnavailable, HTTPStatus: status, Endpoint: endpoint, Message: fmt.Sprintf("媒体资源已失效 (%d)", status)}
	case 412, 418, 432:
		return &site.Error{Site: siteID, Kind: site.ErrKindRiskControl, HTTPStatus: status, Endpoint: endpoint, Message: fmt.Sprintf("媒体请求触发风控 (%d)", status)}
	case 414, 429:
		return &site.Error{Site: siteID, Kind: site.ErrKindRateLimited, HTTPStatus: status, Endpoint: endpoint, Message: fmt.Sprintf("媒体请求触发限流 (%d)", status)}
	default:
		return &site.Error{Site: siteID, Kind: site.ErrKindNetwork, HTTPStatus: status, Endpoint: endpoint, Message: fmt.Sprintf("媒体 HTTP 状态异常 (%d)", status)}
	}
}
