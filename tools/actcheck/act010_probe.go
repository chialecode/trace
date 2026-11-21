package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/sitecheck"
)

// probeOrDownloadMP4 downloads an MP4 in 8 MiB closed Range chunks when <= 24 MiB and validates
// it via EnsureSingleMP4Context; when > 24 MiB, it fetches only the first 8 MiB and last 1 MiB
// via Range to inspect top-level ISO BMFF container boxes (ftyp/moov/mdat).
func probeOrDownloadMP4(
	ctx context.Context,
	client *http.Client,
	spec site.MediaTransportSpec,
	workDir string,
	fileName string,
	mediaURL string,
	hintSize int64,
	expectedDurationSec float64,
	strictDuration bool,
	ck *act010Checker,
) (map[string]any, error) {
	if err := spec.ValidateURL(mediaURL); err != nil {
		return nil, err
	}

	firstEnd := act010RangeChunkBytes - 1
	if hintSize > 0 && hintSize < act010RangeChunkBytes {
		firstEnd = hintSize - 1
	}

	headBytes, status1, totalSize, contentType, err := fetchRangeChunk(ctx, client, spec, mediaURL, 0, firstEnd)
	if err != nil {
		return nil, err
	}
	if ck.stopReason() != "" {
		return nil, fmt.Errorf("stopped: %s", ck.stopReason())
	}
	if len(headBytes) == 0 {
		return nil, errors.New("unexpected 0-byte initial media chunk")
	}
	if totalSize <= 0 && hintSize > 0 {
		totalSize = hintSize
	}
	if totalSize <= 0 && status1 == http.StatusOK {
		totalSize = int64(len(headBytes))
	}

	// Large file (> 24 MiB): fetch ONLY head 8 MiB + tail 1 MiB to inspect MP4 container boxes.
	if totalSize > act010LargeFileCutoff {
		tailStart := totalSize - act010TailProbeBytes
		if tailStart < int64(len(headBytes)) {
			tailStart = int64(len(headBytes))
		}
		var tailBytes []byte
		statuses := []int{status1}
		if tailStart < totalSize {
			var status2 int
			tailBytes, status2, _, _, err = fetchRangeChunk(ctx, client, spec, mediaURL, tailStart, totalSize-1)
			if err != nil {
				return nil, err
			}
			statuses = append(statuses, status2)
		}
		boxSummary, bErr := inspectPartialMP4HeadTail(headBytes, tailStart, tailBytes, totalSize)
		if bErr != nil {
			return nil, bErr
		}
		boxSummary["largeFilePartialProbe"] = true
		boxSummary["totalBytes"] = totalSize
		boxSummary["downloadedBytes"] = int64(len(headBytes) + len(tailBytes))
		boxSummary["rangeRequests"] = len(statuses)
		boxSummary["rangeStatuses"] = statuses
		boxSummary["contentType"] = contentType
		return boxSummary, nil
	}

	// Normal file (<= 24 MiB): assemble full MP4 via 8 MiB closed Range chunks and run EnsureSingleMP4Context.
	outPath := filepath.Join(workDir, fileName)
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(headBytes); err != nil {
		_ = f.Close()
		return nil, err
	}
	written := int64(len(headBytes))
	rangeReqs := 1
	http206Count := 0
	if status1 == http.StatusPartialContent {
		http206Count++
	}

	for status1 == http.StatusPartialContent && totalSize > 0 && written < totalSize {
		nextEnd := written + act010RangeChunkBytes - 1
		if nextEnd >= totalSize {
			nextEnd = totalSize - 1
		}
		chunk, st, _, _, cErr := fetchRangeChunk(ctx, client, spec, mediaURL, written, nextEnd)
		if cErr != nil {
			_ = f.Close()
			return nil, cErr
		}
		if st != http.StatusPartialContent {
			_ = f.Close()
			return nil, fmt.Errorf("media range offset %d expected HTTP 206, got %d", written, st)
		}
		if len(chunk) == 0 {
			_ = f.Close()
			return nil, errors.New("unexpected 0-byte media chunk")
		}
		if _, wErr := f.Write(chunk); wErr != nil {
			_ = f.Close()
			return nil, wErr
		}
		written += int64(len(chunk))
		rangeReqs++
		http206Count++
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	checkDur := 0.0
	if strictDuration {
		checkDur = expectedDurationSec
	}
	muxSum, err := download.EnsureSingleMP4Context(ctx, outPath, checkDur)
	if err != nil {
		return nil, err
	}
	durDiff := 0.0
	if expectedDurationSec > 0 {
		durDiff = math.Abs(muxSum.DurationSec - expectedDurationSec)
	}
	return map[string]any{
		"largeFilePartialProbe": false,
		"bytes":                 muxSum.FileBytes,
		"rangeRequests":         rangeReqs,
		"http206Count":          http206Count,
		"contentType":           contentType,
		"codecs":                muxSum.Codecs,
		"width":                 muxSum.Width,
		"height":                muxSum.Height,
		"moovBeforeMdat":        muxSum.MoovBeforeMdat,
		"hasElst":               muxSum.HasElst,
		"declaredDurationSec":   expectedDurationSec,
		"durationSec":           math.Round(muxSum.DurationSec*1000) / 1000,
		"durationDiffSec":       math.Round(durDiff*1000) / 1000,
	}, nil
}

func fetchRangeChunk(
	ctx context.Context,
	client *http.Client,
	spec site.MediaTransportSpec,
	mediaURL string,
	startByte int64,
	endByte int64,
) ([]byte, int, int64, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, 0, 0, "", err
	}
	spec.ApplyHeaders(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", startByte, endByte))

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, 0, "", err
	}
	defer resp.Body.Close()

	if startByte > 0 {
		if resp.StatusCode != http.StatusPartialContent {
			return nil, resp.StatusCode, 0, resp.Header.Get("Content-Type"), fmt.Errorf("media range offset %d expected HTTP 206, got %d", startByte, resp.StatusCode)
		}
	} else if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, 0, resp.Header.Get("Content-Type"), fmt.Errorf("media HTTP %d", resp.StatusCode)
	}
	maxRead := (endByte - startByte + 1) + (1 << 20)
	if resp.StatusCode == http.StatusOK && maxRead < act010LargeFileCutoff+1024 {
		maxRead = act010LargeFileCutoff + 1024
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRead))
	if err != nil {
		return nil, resp.StatusCode, 0, resp.Header.Get("Content-Type"), err
	}

	totalSize := parseContentRangeTotal(resp.Header.Get("Content-Range"))
	if totalSize <= 0 && resp.ContentLength > 0 && resp.StatusCode == http.StatusOK {
		totalSize = resp.ContentLength
	}
	return data, resp.StatusCode, totalSize, resp.Header.Get("Content-Type"), nil
}

func parseContentRangeTotal(hdr string) int64 {
	// Format: "bytes 0-8388607/31457280"
	_, slashAfter, ok := strings.Cut(strings.TrimSpace(hdr), "/")
	if !ok {
		return 0
	}
	slashAfter = strings.TrimSpace(slashAfter)
	if slashAfter == "" || slashAfter == "*" {
		return 0
	}
	n, err := strconv.ParseInt(slashAfter, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func inspectPartialMP4HeadTail(head []byte, tailOffset int64, tail []byte, totalSize int64) (map[string]any, error) {
	if len(head) < 16 {
		return nil, errors.New("partial MP4 head shorter than 16 bytes")
	}

	readSlice := func(offset int64, n int) ([]byte, bool) {
		if offset < 0 || offset+int64(n) > totalSize {
			return nil, false
		}
		if offset+int64(n) <= int64(len(head)) {
			return head[offset : offset+int64(n)], true
		}
		if len(tail) > 0 && offset >= tailOffset && (offset-tailOffset)+int64(n) <= int64(len(tail)) {
			rel := offset - tailOffset
			return tail[rel : rel+int64(n)], true
		}
		return nil, false
	}

	var boxes []string
	hasFtyp, hasMoov, hasMdat := false, false, false
	moovIdx, mdatIdx := -1, -1
	var offset int64
	for stepCount := 0; offset < totalSize && stepCount < 64; stepCount++ {
		hdr, ok := readSlice(offset, 8)
		if !ok {
			break
		}
		sz32 := binary.BigEndian.Uint32(hdr[0:4])
		boxType := string(hdr[4:8])
		var boxSize int64
		switch sz32 {
		case 0:
			boxSize = totalSize - offset
		case 1:
			ext, okExt := readSlice(offset+8, 8)
			if !okExt {
				boxSize = 0
			} else {
				u64 := binary.BigEndian.Uint64(ext)
				if u64 > uint64(totalSize-offset) || u64 < 16 {
					return nil, fmt.Errorf("invalid 64-bit MP4 box size in %q", boxType)
				}
				boxSize = int64(u64)
			}
		default:
			if sz32 < 8 {
				return nil, fmt.Errorf("invalid MP4 box size %d in %q", sz32, boxType)
			}
			boxSize = int64(sz32)
		}
		if boxSize <= 0 || offset+boxSize > totalSize {
			break
		}

		boxes = append(boxes, boxType)
		switch boxType {
		case "ftyp":
			hasFtyp = true
		case "moov":
			hasMoov = true
			if moovIdx < 0 {
				moovIdx = len(boxes) - 1
			}
		case "mdat":
			hasMdat = true
			if mdatIdx < 0 {
				mdatIdx = len(boxes) - 1
			}
		}
		offset += boxSize
	}

	if !hasFtyp || (!hasMoov && !hasMdat) {
		return nil, fmt.Errorf("partial MP4 missing expected ISO BMFF boxes (got %v)", boxes)
	}

	return map[string]any{
		"boxes":          boxes,
		"hasFtyp":        hasFtyp,
		"hasMoov":        hasMoov,
		"hasMdat":        hasMdat,
		"moovBeforeMdat": hasMoov && hasMdat && moovIdx < mdatIdx,
	}, nil
}

func fetchAssetSample(
	ctx context.Context,
	client *http.Client,
	spec site.MediaTransportSpec,
	assetURL string,
	limitBytes int64,
) (map[string]any, error) {
	if err := spec.ValidateURL(assetURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	spec.ApplyHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("asset HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limitBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limitBytes {
		return nil, fmt.Errorf("asset exceeded %d byte cap", limitBytes)
	}
	if len(raw) == 0 {
		return nil, errors.New("asset returned 0 bytes")
	}

	return map[string]any{
		"status":      resp.StatusCode,
		"contentType": resp.Header.Get("Content-Type"),
		"magicFormat": sniffAssetMagic(raw),
		"bytes":       len(raw),
	}, nil
}

func sniffAssetMagic(raw []byte) string {
	switch {
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "webp"
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "jpeg"
	case len(raw) >= 8 && string(raw[0:8]) == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(raw) >= 12 && string(raw[4:8]) == "ftyp":
		brand := strings.TrimSpace(string(raw[8:12]))
		switch brand {
		case "heic", "heix", "mif1", "msf1":
			return "heic"
		case "M4A", "mp42", "isom", "iso2":
			return "mp4_or_m4a"
		default:
			return "bmff:" + brand
		}
	case len(raw) >= 3 && string(raw[0:3]) == "ID3":
		return "mp3"
	case len(raw) >= 2 && raw[0] == 0xFF && (raw[1]&0xE0) == 0xE0:
		return "aac_or_mp3_adts"
	default:
		return "other"
	}
}

func inspectRoomStreamCapabilities(room *douyin.LiveRoomDetail, allowLocalhost bool) map[string]any {
	out := map[string]any{
		"originHasFLV": false,
		"originHasHLS": false,
	}
	if room == nil || room.StreamURL == nil {
		return out
	}

	var tiers []string
	var sampleHLS string
	if room.StreamURL.LiveCoreSDK != nil && room.StreamURL.LiveCoreSDK.PullData != nil &&
		strings.TrimSpace(room.StreamURL.LiveCoreSDK.PullData.StreamData) != "" {
		var parsed struct {
			Data map[string]map[string]struct {
				FLV string `json:"flv"`
				HLS string `json:"hls"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(room.StreamURL.LiveCoreSDK.PullData.StreamData), &parsed) == nil {
			for tier, lines := range parsed.Data {
				tiers = append(tiers, tier)
				mainLine := lines["main"]
				if strings.EqualFold(tier, "origin") {
					out["originHasFLV"] = strings.TrimSpace(mainLine.FLV) != ""
					out["originHasHLS"] = strings.TrimSpace(mainLine.HLS) != ""
					if sampleHLS == "" && strings.TrimSpace(mainLine.HLS) != "" {
						sampleHLS = strings.TrimSpace(mainLine.HLS)
					}
				}
			}
			sort.Strings(tiers)
		}
	}
	if sampleHLS == "" && strings.TrimSpace(room.StreamURL.HLSPullURL) != "" {
		sampleHLS = strings.TrimSpace(room.StreamURL.HLSPullURL)
	}
	if sampleHLS == "" {
		for _, v := range room.StreamURL.HLSPullURLMap {
			if strings.TrimSpace(v) != "" {
				sampleHLS = strings.TrimSpace(v)
				break
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(sampleHLS), "http://") && !allowLocalhost {
		sampleHLS = "https://" + sampleHLS[len("http://"):]
	}
	if len(tiers) > 0 {
		out["sdkTiers"] = tiers
	}
	if sampleHLS != "" {
		out["sampleHlsUrl"] = sampleHLS
	}
	return out
}

type douyinLiveProbeOptions struct {
	allowLocalhost   bool
	fetchHLS         bool
	recordHLSCount   bool
	recordFor        time.Duration
	workDir          string
	allowReconnect   bool
	reconnectWebRID  string
	maxLiveReqs      int
	maxWebEnterCalls int
	redirectBlocked  *string
}

// probeAndRecordDouyinLive runs the shared V-07 inspection (stream capabilities,
// optional HLS playlist probe, and bounded HTTP-FLV recording with optional
// single reconnect refresh).
func probeAndRecordDouyinLive(
	ctx context.Context,
	activeRoom *douyin.LiveRoomDetail,
	dyClient *douyin.Client,
	mediaHTTP *http.Client,
	liveHTTP *http.Client,
	gate *sitecheck.Gate,
	ck *act010Checker,
	redactor *act010Redactor,
	rc *act010Receipt,
	f map[string]any,
	opts douyinLiveProbeOptions,
) error {
	if activeRoom == nil || activeRoom.RoomStatus != 2 {
		f["status"] = "not-run"
		f["reason"] = "live room offline"
		rc.Verifications["V-07"] = map[string]any{
			"status": "not-run",
			"reason": "live room offline",
		}
		return errSkip
	}
	if activeRoom.AudioOnly {
		f["status"] = "not-run"
		f["reason"] = "live room in audio-only mode"
		rc.Verifications["V-07"] = map[string]any{
			"status": "not-run",
			"reason": "live room in audio-only mode",
		}
		return errSkip
	}

	streamCaps := inspectRoomStreamCapabilities(activeRoom, opts.allowLocalhost)
	f["streamCapabilities"] = streamCaps

	liveSpec := dyClient.LiveTransport()
	var hlsSummary map[string]any
	if hlsURL, _ := streamCaps["sampleHlsUrl"].(string); hlsURL != "" && opts.fetchHLS {
		delete(streamCaps, "sampleHlsUrl")
		req, rErr := http.NewRequestWithContext(ctx, http.MethodGet, hlsURL, nil)
		if rErr == nil {
			liveSpec.ApplyHeaders(req)
			resp, hErr := mediaHTTP.Do(req)
			if hErr != nil {
				if ck.stopReason() != "" || errors.Is(hErr, sitecheck.ErrQuotaExceeded) {
					return hErr
				}
			} else {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				ck.mu.Lock()
				if len(ck.playlists) > 0 {
					hlsSummary = ck.playlists[len(ck.playlists)-1]
				}
				ck.mu.Unlock()
			}
		}
	} else {
		delete(streamCaps, "sampleHlsUrl")
	}

	plan, err := douyin.SelectFLVStreamFromRoom(activeRoom, nil, liveSpec, opts.allowLocalhost)
	if err != nil {
		return err
	}

	ck.liveStreamActive.Store(true)
	defer ck.liveStreamActive.Store(false)

	recSummary, recErr := recordShortFLV(ctx, liveHTTP, plan, opts.workDir, opts.recordFor, ck)
	if opts.allowReconnect && recErr != nil && ck.stopReason() == "" && !errors.Is(recErr, sitecheck.ErrQuotaExceeded) &&
		int(ck.live.requests.Load()) < opts.maxLiveReqs && gate.Usage().Categories["web_enter"] < opts.maxWebEnterCalls {
		// Use the single authorized web_enter reconnect spare to refresh FLV stream URLs once.
		ck.liveStreamActive.Store(false)
		refreshed, reErr := dyClient.EnterLiveRoom(ctx, opts.reconnectWebRID, activeRoom.OwnerSecUID)
		ck.liveStreamActive.Store(true)
		if reErr == nil && refreshed.RoomStatus == 2 && !refreshed.AudioOnly {
			redactor.addIDs(refreshed.OwnerSecUID, refreshed.OwnerUID, refreshed.OwnerShortID, refreshed.ResponseWebRID, refreshed.RoomID)
			if refreshedPlan, pErr := douyin.SelectFLVStreamFromRoom(refreshed, nil, liveSpec, opts.allowLocalhost); pErr == nil {
				recSummary, recErr = recordShortFLV(ctx, liveHTTP, refreshedPlan, opts.workDir, opts.recordFor, ck)
			}
		}
	}
	if opts.redirectBlocked != nil && *opts.redirectBlocked != "" {
		f["productionRedirectBlocked"] = *opts.redirectBlocked
	}
	if recErr != nil {
		return recErr
	}

	if hlsSummary != nil {
		recSummary["hlsPlaylist"] = hlsSummary
	}
	if opts.recordHLSCount && ck.hls != nil {
		recSummary["hlsPlaylistRequests"] = int(ck.hls.requests.Load())
	}
	recSummary["originHasFLV"] = streamCaps["originHasFLV"]
	recSummary["originHasHLS"] = streamCaps["originHasHLS"]
	recSummary["status"] = "completed"
	for k, v := range recSummary {
		f[k] = v
	}
	rc.Verifications["V-07"] = recSummary
	return nil
}

func recordShortFLV(
	parentCtx context.Context,
	client *http.Client,
	plan site.LiveStreamPlan,
	workDir string,
	recordFor time.Duration,
	ck *act010Checker,
) (map[string]any, error) {
	if len(plan.URLs) == 0 {
		return nil, errors.New("no live FLV URL in stream plan")
	}

	maxAttempts := len(plan.URLs)
	if maxAttempts > act010MaxLiveConns {
		maxAttempts = act010MaxLiveConns
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ck.stopReason() != "" {
			return nil, fmt.Errorf("stopped: %s", ck.stopReason())
		}
		streamURL := plan.URLs[attempt]
		if err := plan.Media.ValidateURL(streamURL); err != nil {
			lastErr = err
			continue
		}

		recCtx, cancel := context.WithTimeout(parentCtx, recordFor)
		req, err := http.NewRequestWithContext(recCtx, http.MethodGet, streamURL, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		plan.Media.ApplyHeaders(req)

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			if ck.stopReason() != "" || errors.Is(err, sitecheck.ErrQuotaExceeded) {
				return nil, err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			cancel()
			lastErr = fmt.Errorf("live FLV HTTP %d", resp.StatusCode)
			if ck.stopReason() != "" {
				return nil, lastErr
			}
			continue
		}

		partPath := filepath.Join(workDir, fmt.Sprintf("live-part-%03d.mp4.part", attempt+1))
		partFile, err := os.OpenFile(partPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = resp.Body.Close()
			cancel()
			return nil, err
		}

		var wireBuf bytes.Buffer
		tee := io.TeeReader(resp.Body, &cappedBufferWriter{buf: &wireBuf, max: 256 * 1024})
		remuxer := live.NewFLVRemuxer(tee, live.FLVRemuxOptions{})
		cp, remuxErr := remuxer.WritePart(recCtx, partFile, nil)
		_ = resp.Body.Close()
		_ = partFile.Close()
		timedOut := recCtx.Err() != nil
		cancel()

		if cp.BytesWritten == 0 {
			if remuxErr != nil {
				lastErr = remuxErr
			} else {
				lastErr = errors.New("live FLV produced 0 remuxed bytes")
			}
			continue
		}
		// Normal time-bounded stop after recordFor when at least 1 fragment was written.
		if remuxErr != nil && !timedOut && !errors.Is(remuxErr, live.ErrFLVTruncated) {
			return nil, remuxErr
		}

		finalPath := filepath.Join(workDir, fmt.Sprintf("live-part-%03d.mp4", attempt+1))
		mp4Sum, err := defragmentLivePartFile(parentCtx, partPath, finalPath)
		if err != nil {
			return nil, err
		}

		wireHeader := detectFLVWireVideoFormat(wireBuf.Bytes())
		return map[string]any{
			"selectedQuality":    plan.QualityName,
			"selectedCodec":      plan.Codec,
			"wireVideoTagFormat": wireHeader,
			"remuxFourCC":        string(cp.FourCC),
			"partCount":          1,
			"fragments":          cp.Sequence,
			"partBytes":          cp.BytesWritten,
			"mp4Bytes":           mp4Sum.FileBytes,
			"codecs":             mp4Sum.Codecs,
			"width":              mp4Sum.Width,
			"height":             mp4Sum.Height,
			"moovBeforeMdat":     mp4Sum.MoovBeforeMdat,
			"durationSec":        math.Round(mp4Sum.DurationSec*1000) / 1000,
		}, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("live FLV recording failed")
}

type cappedBufferWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *cappedBufferWriter) Write(p []byte) (int, error) {
	rem := w.max - w.buf.Len()
	if rem > 0 {
		if len(p) <= rem {
			_, _ = w.buf.Write(p)
		} else {
			_, _ = w.buf.Write(p[:rem])
		}
	}
	return len(p), nil
}

func detectFLVWireVideoFormat(sample []byte) string {
	fr := live.NewFLVReader(bytes.NewReader(sample))
	for i := 0; i < 64; i++ {
		tag, err := fr.NextTag()
		if err != nil {
			break
		}
		if tag.Type != 9 || len(tag.Data) == 0 {
			continue
		}
		b0 := tag.Data[0]
		if b0&0x80 != 0 {
			if len(tag.Data) >= 5 {
				fourCC := string(tag.Data[1:5])
				if fourCC == "hvc1" || fourCC == "hev1" {
					return "hevc-" + fourCC
				}
				return "exflv-" + fourCC
			}
			return "exflv"
		}
		switch b0 & 0x0f {
		case 7:
			return "avc1"
		case 12:
			return "hevc-cid12"
		default:
			return fmt.Sprintf("codec-%d", b0&0x0f)
		}
	}
	return "unknown"
}

func defragmentLivePartFile(ctx context.Context, partPath, outPath string) (download.MuxTrackSummary, error) {
	inFile, err := os.Open(partPath)
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	defer inFile.Close()

	parsed, err := mp4.DecodeFile(inFile, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	outFile, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	if err := mp4.Defragment(parsed, inFile, outFile); err != nil {
		_ = outFile.Close()
		return download.MuxTrackSummary{}, err
	}
	if err := outFile.Sync(); err != nil {
		_ = outFile.Close()
		return download.MuxTrackSummary{}, err
	}
	if err := outFile.Close(); err != nil {
		return download.MuxTrackSummary{}, err
	}

	summary, err := download.InspectMP4FileContext(ctx, outPath)
	if err != nil {
		return summary, err
	}
	hasVideo := false
	for _, h := range summary.Handlers {
		if h == "vide" {
			hasVideo = true
		}
	}
	if !hasVideo || !summary.MoovBeforeMdat || summary.DurationSec <= 0 {
		return summary, errors.New("finalized live MP4 failed track, moov, or duration check")
	}
	return summary, nil
}
