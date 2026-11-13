package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/sitecheck"
)

const (
	synthAVCSPSHex   = "6764001eacd940a02ff9610000030001000003003c8f162d96"
	synthAVCPPSHex   = "68ebecb22c"
	synthAVCSliceHex = "658884004ffede23c0a6109524aac694"

	synthHEVCVPSHex   = "40010c01ffff016000000300900000030000030078959809"
	synthHEVCSPSHex   = "420101016000000300900000030000030078a00502016965959a4932bc05a80808082000000300200000030321"
	synthHEVCPPSHex   = "4401c172b46240"
	synthHEVCIDRHex   = "2601af0940a1b2c3d4e5f601"
	synthHEVCTrailHex = "0201d0094011223344556677"

	synthSecUIDSelf  = "MS4wLjABAAAA_SYNTH_SELF_ACCOUNT_010"
	synthSecUIDUser1 = "MS4wLjABAAAA_SYNTH_CREATOR_ONE_001"
	synthSecUIDUser2 = "MS4wLjABAAAA_SYNTH_CREATOR_TWO_002"
	synthUIDSelf     = "910000000010"
	synthUIDUser1    = "920000000001"
	synthUIDUser2    = "920000000002"
	synthShortID1    = "8810001"
	synthShortID2    = "8820002"
	synthAltWebRID   = "776655443322"
	synthRoomIDLive  = "7411111111111119999"
	synthAwemePin    = "7300000000000001001"
	synthAwemeRecent = "7300000000000001002"
	synthAwemeNote   = "7300000000000001003"
	synthAwemeMixed  = "7300000000000001004"
	synthCookieRaw   = "sessionid=SENTINEL_SESSION_010; sessionid_ss=SENTINEL_SESSION_SS_010; sid_guard=SENTINEL_GUARD_010; ttwid=SENTINEL_TTWID_010; UIFID=SENTINEL_UIFID_010; is_staff_user=false; login_time=1762900000; dy_swidth=1920; dy_sheight=1080; bd_ticket_guard_client_data=SENTINEL_TICKET_GUARD_010"
)

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode failed: %v", err)
	}
	return raw
}

func buildSyntheticProgressiveMP4(t *testing.T, durationSec int, padFreeBytes int) []byte {
	t.Helper()
	dir := t.TempDir()
	vBytes, err := download.BuildSyntheticVideoM4S(durationSec*30, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	vPath := filepath.Join(dir, "v.m4s")
	if err := os.WriteFile(vPath, vBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	aBytes, err := download.BuildSyntheticAudioM4S(durationSec * 47)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}
	aPath := filepath.Join(dir, "a.m4s")
	if err := os.WriteFile(aPath, aBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "prog.mp4")
	if _, err := download.MuxDASHFiles(vPath, aPath, outPath); err != nil {
		t.Fatalf("MuxDASHFiles: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if padFreeBytes >= 8 {
		freeBox := make([]byte, padFreeBytes)
		binary.BigEndian.PutUint32(freeBox[0:4], uint32(padFreeBytes))
		copy(freeBox[4:8], "free")
		raw = append(raw, freeBox...)
	}
	return raw
}

type virtualLargeMP4 struct {
	ftypBox   []byte
	moovBox   []byte
	totalSize int64
}

func newVirtualLargeMP4(t *testing.T, baseMP4 []byte, totalSize int64) *virtualLargeMP4 {
	t.Helper()
	// Extract real ftyp and moov boxes from baseMP4, and place a large virtual mdat in between:
	// [ftyp][mdat (totalSize - len(ftyp) - len(moov))][moov]
	var ftypBytes, moovBytes []byte
	pos := 0
	for pos+8 <= len(baseMP4) {
		sz := int(binary.BigEndian.Uint32(baseMP4[pos : pos+4]))
		typ := string(baseMP4[pos+4 : pos+8])
		if sz < 8 || pos+sz > len(baseMP4) {
			break
		}
		switch typ {
		case "ftyp":
			ftypBytes = append([]byte(nil), baseMP4[pos:pos+sz]...)
		case "moov":
			moovBytes = append([]byte(nil), baseMP4[pos:pos+sz]...)
		}
		pos += sz
	}
	if len(ftypBytes) == 0 || len(moovBytes) == 0 {
		t.Fatalf("baseMP4 missing ftyp or moov")
	}
	return &virtualLargeMP4{
		ftypBox:   ftypBytes,
		moovBox:   moovBytes,
		totalSize: totalSize,
	}
}

func (v *virtualLargeMP4) byteAt(offset int64) byte {
	ftypLen := int64(len(v.ftypBox))
	moovStart := v.totalSize - int64(len(v.moovBox))
	mdatSize := moovStart - ftypLen

	switch {
	case offset < ftypLen:
		return v.ftypBox[offset]
	case offset >= moovStart && offset < v.totalSize:
		return v.moovBox[offset-moovStart]
	case offset >= ftypLen && offset < ftypLen+8:
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:4], uint32(mdatSize))
		copy(hdr[4:8], "mdat")
		return hdr[offset-ftypLen]
	default:
		return 0
	}
}

func (v *virtualLargeMP4) serveRange(w http.ResponseWriter, r *http.Request, middleCalls *atomic.Int64) {
	rng := r.Header.Get("Range")
	if !strings.HasPrefix(rng, "bytes=") {
		http.Error(w, "Range required for virtual large MP4", http.StatusBadRequest)
		return
	}
	startStr, endStr, ok := strings.Cut(strings.TrimPrefix(rng, "bytes="), "-")
	if !ok {
		http.Error(w, "bad Range", http.StatusBadRequest)
		return
	}
	start, err1 := strconv.ParseInt(startStr, 10, 64)
	end, err2 := strconv.ParseInt(endStr, 10, 64)
	if err1 != nil || err2 != nil || start < 0 || end < start {
		http.Error(w, "invalid Range bounds", http.StatusBadRequest)
		return
	}
	if end >= v.totalSize {
		end = v.totalSize - 1
	}
	if start > 0 && start < v.totalSize-(1<<20) && middleCalls != nil {
		middleCalls.Add(1)
	}

	out := make([]byte, end-start+1)
	for i := range out {
		out[i] = v.byteAt(start + int64(i))
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, v.totalSize))
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(out)
}

func serveBytesWithRange(w http.ResponseWriter, r *http.Request, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Accept-Ranges", "bytes")
	rng := r.Header.Get("Range")
	if strings.HasPrefix(rng, "bytes=") {
		startStr, endStr, ok := strings.Cut(strings.TrimPrefix(rng, "bytes="), "-")
		if ok {
			start, err1 := strconv.ParseInt(startStr, 10, 64)
			end, err2 := strconv.ParseInt(endStr, 10, 64)
			if err1 == nil && err2 == nil && start >= 0 && start < int64(len(data)) {
				if end >= int64(len(data)) {
					end = int64(len(data)) - 1
				}
				if end >= start {
					slice := data[start : end+1]
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
					w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write(slice)
					return
				}
			}
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func buildSyntheticEnhancedHEVCFLV(t *testing.T) []byte {
	t.Helper()
	vps := mustDecodeHex(t, synthHEVCVPSHex)
	sps := mustDecodeHex(t, synthHEVCSPSHex)
	pps := mustDecodeHex(t, synthHEVCPPSHex)
	idr := mustDecodeHex(t, synthHEVCIDRHex)
	trail := mustDecodeHex(t, synthHEVCTrailHex)

	hvcC, err := hevc.CreateHEVCDecConfRec([][]byte{vps}, [][]byte{sps}, [][]byte{pps}, true, true, true, true)
	if err != nil {
		t.Fatalf("CreateHEVCDecConfRec: %v", err)
	}
	var cfgRaw bytes.Buffer
	if err := hvcC.Encode(&cfgRaw); err != nil {
		t.Fatalf("encode hvcC: %v", err)
	}

	var flv bytes.Buffer
	// Header + PreviousTagSize0
	flv.Write([]byte{'F', 'L', 'V', 0x01, 0x05, 0x00, 0x00, 0x00, 0x09, 0x00, 0x00, 0x00, 0x00})

	writeTag := func(tagType uint8, ts uint32, payload []byte) {
		var hdr [11]byte
		sz := uint32(len(payload))
		hdr[0] = tagType
		hdr[1] = byte(sz >> 16)
		hdr[2] = byte(sz >> 8)
		hdr[3] = byte(sz)
		hdr[4] = byte(ts >> 16)
		hdr[5] = byte(ts >> 8)
		hdr[6] = byte(ts)
		hdr[7] = byte(ts >> 24)
		flv.Write(hdr[:])
		flv.Write(payload)
		var prev [4]byte
		binary.BigEndian.PutUint32(prev[:], 11+sz)
		flv.Write(prev[:])
	}

	// AAC sequence header (48 kHz stereo)
	writeTag(8, 0, []byte{0xAF, 0x00, 0x11, 0x90})

	// Enhanced FLV SequenceStart (IsExHeader=1 | FrameType=1(key) | PacketType=0 => 0x90, FourCC="hvc1")
	seqBody := append([]byte{0x90, 'h', 'v', 'c', '1'}, cfgRaw.Bytes()...)
	writeTag(9, 0, seqBody)

	packNALU := func(nalu []byte) []byte {
		out := make([]byte, 4+len(nalu))
		binary.BigEndian.PutUint32(out[0:4], uint32(len(nalu)))
		copy(out[4:], nalu)
		return out
	}

	// Emit 2 GOPs (0ms..2400ms) so FLVRemuxer flushes complete fragments cleanly.
	for i := 0; i < 6; i++ {
		ts := uint32(i * 400)
		isKey := i%3 == 0
		var frameHdr byte
		var nalu []byte
		if isKey {
			frameHdr = 0x90 | 0x03 // ExHeader + Keyframe(1<<4) + CodedFramesX(3)
			nalu = idr
		} else {
			frameHdr = 0x80 | 0x20 | 0x03 // ExHeader + Inter(2<<4) + CodedFramesX(3)
			nalu = trail
		}
		vBody := append([]byte{frameHdr, 'h', 'v', 'c', '1'}, packNALU(nalu)...)
		writeTag(9, ts, vBody)
		writeTag(8, ts, []byte{0xAF, 0x01, 0x21, 0x10, 0x04, 0x60, 0x8c, 0x1c})
	}

	return flv.Bytes()
}

type act010MockEnv struct {
	rootDir           string
	srv               *httptest.Server
	endpoints         douyin.Endpoints
	roomLive          bool
	middleRangeCalls  atomic.Int64
	cookieLeakOnMedia atomic.Int64
	totalRequests     atomic.Int64
	mu                sync.Mutex
	reqTimes          []time.Time
	profileOtherHook  func(w http.ResponseWriter, r *http.Request) bool
	liveFLVHook       func(w http.ResponseWriter, r *http.Request) bool
}

func setupAct010MockEnv(t *testing.T, roomLive bool) *act010MockEnv {
	t.Helper()
	rootDir := t.TempDir()
	env := &act010MockEnv{
		rootDir:  rootDir,
		roomLive: roomLive,
	}

	// Build synthetic MP4 (~8.5 MiB -> forces 2 closed 8 MiB Range chunks: 8 MiB + ~0.5 MiB!)
	normalVideoMP4 := buildSyntheticProgressiveMP4(t, 2, (8<<20)+(512<<10))
	smallClipMP4 := buildSyntheticProgressiveMP4(t, 2, 1024)
	largeVirtual := newVirtualLargeMP4(t, smallClipMP4, 30<<20) // 30 MiB (> 24 MiB cutoff!)
	liveFLVBytes := buildSyntheticEnhancedHEVCFLV(t)

	webpSample := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
	mp3Sample := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x10"), make([]byte, 64)...)

	mux := http.NewServeMux()
	env.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.totalRequests.Add(1)
		env.mu.Lock()
		env.reqTimes = append(env.reqTimes, time.Now())
		env.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(env.srv.Close)

	base := env.srv.URL
	env.endpoints = douyin.Endpoints{
		ProfileSelf:    base + "/aweme/v1/web/user/profile/self/",
		ProfileOther:   base + "/aweme/v1/web/user/profile/other/",
		AwemePost:      base + "/aweme/v1/web/aweme/post/",
		AwemeDetail:    base + "/aweme/v1/web/aweme/detail/",
		LiveWebEnter:   base + "/webcast/room/web/enter/",
		LiveReflowInfo: base + "/webcast/room/reflow/info/",
		LiveCheckAlive: base + "/webcast/room/check_alive/",
	}

	writeJSON := func(w http.ResponseWriter, payload any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(payload)
	}

	mux.HandleFunc("/aweme/v1/web/user/profile/self/", func(w http.ResponseWriter, r *http.Request) {
		ck := r.Header.Get("Cookie")
		if !strings.Contains(ck, "SENTINEL_SESSION_010") {
			writeJSON(w, map[string]any{
				"status_code": 2483,
				"status_msg":  "未登录",
				"user":        nil,
			})
			return
		}
		w.Header().Set("X-Ms-Token", "SYNTH_REFRESHED_MSTOKEN_010")
		writeJSON(w, map[string]any{
			"status_code": 0,
			"user": map[string]any{
				"uid":       synthUIDSelf,
				"sec_uid":   synthSecUIDSelf,
				"unique_id": "synth_self_id",
				"short_id":  "8800010",
				"nickname":  "测试登录账号",
				"avatar_thumb": map[string]any{
					"url_list": []string{base + "/cdn/img/self.webp"},
				},
			},
		})
	})

	mux.HandleFunc("/aweme/v1/web/user/profile/other/", func(w http.ResponseWriter, r *http.Request) {
		if env.profileOtherHook != nil && env.profileOtherHook(w, r) {
			return
		}
		sec := r.URL.Query().Get("sec_user_id")
		if sec == synthSecUIDUser1 {
			writeJSON(w, map[string]any{
				"status_code": 0,
				"user": map[string]any{
					"uid":         synthUIDUser1,
					"sec_uid":     synthSecUIDUser1,
					"unique_id":   "creator_one",
					"short_id":    synthShortID1,
					"nickname":    "创作者一",
					"aweme_count": 3,
					"live_status": 0,
					"room_id_str": "0",
				},
			})
			return
		}
		roomIDStr := "0"
		liveStatus := 0
		if env.roomLive {
			roomIDStr = synthRoomIDLive
			liveStatus = 1
		}
		writeJSON(w, map[string]any{
			"status_code": 0,
			"user": map[string]any{
				"uid":         synthUIDUser2,
				"sec_uid":     synthSecUIDUser2,
				"unique_id":   "creator_two",
				"short_id":    synthShortID2,
				"nickname":    "创作者二",
				"aweme_count": 2,
				"live_status": liveStatus,
				"room_id_str": roomIDStr,
			},
		})
	})

	mux.HandleFunc("/aweme/v1/web/aweme/post/", func(w http.ResponseWriter, r *http.Request) {
		sec := r.URL.Query().Get("sec_user_id")
		cursor := r.URL.Query().Get("max_cursor")
		if sec == synthSecUIDUser1 && (cursor == "" || cursor == "0") {
			writeJSON(w, map[string]any{
				"status_code": 0,
				"has_more":    1,
				"max_cursor":  1700000100000,
				"min_cursor":  1700000200000,
				"aweme_list": []any{
					mockVideoAwemeJSON(synthAwemePin, synthSecUIDUser1, synthUIDUser1, 1, 2000, base+"/cdn/vod/recent.mp4", int64(len(normalVideoMP4))),
					mockVideoAwemeJSON(synthAwemeRecent, synthSecUIDUser1, synthUIDUser1, 0, 2000, base+"/cdn/vod/recent.mp4", int64(len(normalVideoMP4))),
				},
			})
			return
		}
		if sec == synthSecUIDUser1 {
			// Include synthAwemePin again to verify cross-page deduplication!
			writeJSON(w, map[string]any{
				"status_code": 0,
				"has_more":    0,
				"max_cursor":  1700000050000,
				"min_cursor":  1700000100000,
				"aweme_list": []any{
					mockVideoAwemeJSON(synthAwemePin, synthSecUIDUser1, synthUIDUser1, 1, 2000, base+"/cdn/vod/recent.mp4", int64(len(normalVideoMP4))),
					mockVideoAwemeJSON("7300000000000001099", synthSecUIDUser1, synthUIDUser1, 0, 2000, base+"/cdn/vod/recent.mp4", int64(len(normalVideoMP4))),
				},
			})
			return
		}
		writeJSON(w, map[string]any{
			"status_code": 0,
			"has_more":    0,
			"max_cursor":  1700000010000,
			"min_cursor":  1700000010000,
			"aweme_list": []any{
				mockVideoAwemeJSON("7300000000000002001", synthSecUIDUser2, synthUIDUser2, 0, 2000, base+"/cdn/vod/recent.mp4", int64(len(normalVideoMP4))),
			},
		})
	})

	mux.HandleFunc("/aweme/v1/web/aweme/detail/", func(w http.ResponseWriter, r *http.Request) {
		aid := r.URL.Query().Get("aweme_id")
		switch aid {
		case synthAwemePin:
			writeJSON(w, map[string]any{
				"status_code":  0,
				"aweme_detail": mockVideoAwemeJSON(synthAwemePin, synthSecUIDUser1, synthUIDUser1, 1, 2000, base+"/cdn/vod/large.mp4", 30<<20),
			})
		case synthAwemeRecent:
			writeJSON(w, map[string]any{
				"status_code":  0,
				"aweme_detail": mockVideoAwemeJSON(synthAwemeRecent, synthSecUIDUser1, synthUIDUser1, 0, 2000, base+"/cdn/vod/recent.mp4", int64(len(normalVideoMP4))),
			})
		case synthAwemeNote:
			writeJSON(w, map[string]any{
				"status_code": 0,
				"aweme_detail": map[string]any{
					"aweme_id":    synthAwemeNote,
					"aweme_type":  68,
					"desc":        "普通实况图文",
					"create_time": 1700000300,
					"author": map[string]any{
						"uid":      synthUIDUser1,
						"sec_uid":  synthSecUIDUser1,
						"nickname": "创作者一",
					},
					"images": []any{
						map[string]any{
							"width":           1080,
							"height":          1440,
							"url_list":        []string{base + "/cdn/img/note1.webp"},
							"live_photo_type": 1,
							"clip_type":       5,
							"video": map[string]any{
								"width":    1080,
								"height":   1440,
								"duration": 2000,
								"play_addr": map[string]any{
									"url_list":  []string{base + "/cdn/vod/livephoto.mp4"},
									"width":     1080,
									"height":    1440,
									"data_size": len(smallClipMP4),
								},
							},
						},
					},
					"statistics": map[string]any{"digg_count": 12, "play_count": 0},
				},
			})
		case synthAwemeMixed:
			writeJSON(w, map[string]any{
				"status_code": 0,
				"aweme_detail": map[string]any{
					"aweme_id":    synthAwemeMixed,
					"aweme_type":  68,
					"desc":        "混排图文视频页",
					"create_time": 1700000400,
					"author": map[string]any{
						"uid":      synthUIDUser2,
						"sec_uid":  synthSecUIDUser2,
						"nickname": "创作者二",
					},
					"images": []any{
						map[string]any{
							"width":    1080,
							"height":   1440,
							"url_list": []string{base + "/cdn/img/mixed1.webp"},
						},
						map[string]any{
							"width":           1080,
							"height":          1920,
							"url_list":        []string{base + "/cdn/img/mixed2_cover.webp"},
							"live_photo_type": 0,
							"video": map[string]any{
								"width":    1080,
								"height":   1920,
								"duration": 2000,
								"play_addr": map[string]any{
									"url_list":  []string{base + "/cdn/vod/large.mp4"},
									"width":     1080,
									"height":    1920,
									"data_size": 30 << 20,
								},
							},
						},
					},
					"music": map[string]any{
						"id_str":            "7500000000000008888",
						"title":             "原声配乐",
						"author":            "创作者二",
						"duration":          15,
						"is_original_sound": true,
						"play_url": map[string]any{
							"url_list": []string{base + "/cdn/audio/bgm.mp3"},
						},
					},
					"statistics": map[string]any{"digg_count": 34, "play_count": 0},
				},
			})
		}
	})

	streamDataJSON := fmt.Sprintf(`{"data":{"origin":{"main":{"flv":%q,"hls":%q,"sdk_params":"{\"VCodec\":\"h265\"}"}}}}`,
		base+"/cdn/live/origin.flv", base+"/cdn/live/origin.m3u8")

	mux.HandleFunc("/webcast/room/web/enter/", func(w http.ResponseWriter, r *http.Request) {
		wr := r.URL.Query().Get("web_rid")
		if wr != synthShortID2 && wr != synthAltWebRID {
			writeJSON(w, map[string]any{"status_code": 4001038, "data": map[string]any{}})
			return
		}
		roomStatus := 4
		outerRoomStatus := 2
		roomIDStr := "0"
		var streamNode any
		if env.roomLive {
			roomStatus = 2
			outerRoomStatus = 0
			roomIDStr = synthRoomIDLive
			streamNode = map[string]any{
				"hls_pull_url": base + "/cdn/live/origin.m3u8",
				"live_core_sdk_data": map[string]any{
					"pull_data": map[string]any{
						"stream_data": streamDataJSON,
					},
				},
			}
		}
		writeJSON(w, map[string]any{
			"status_code": 0,
			"data": map[string]any{
				"enter_mode":  1,
				"room_status": outerRoomStatus,
				"web_rid":     synthShortID2,
				"user": map[string]any{
					"id_str":   synthUIDUser2,
					"sec_uid":  synthSecUIDUser2,
					"short_id": synthShortID2,
					"nickname": "创作者二",
				},
				"data": []any{
					map[string]any{
						"id_str":     roomIDStr,
						"status":     roomStatus,
						"title":      "合成测试直播间",
						"start_time": 1700000500,
						"owner": map[string]any{
							"id_str":   synthUIDUser2,
							"sec_uid":  synthSecUIDUser2,
							"short_id": synthShortID2,
							"web_rid":  synthShortID2,
							"nickname": "创作者二",
						},
						"stream_url": streamNode,
					},
				},
			},
		})
	})

	mux.HandleFunc("/webcast/room/reflow/info/", func(w http.ResponseWriter, r *http.Request) {
		rid := r.URL.Query().Get("room_id")
		if rid == "2" {
			writeJSON(w, map[string]any{
				"status_code": 10011,
				"data":        map[string]any{},
			})
			return
		}
		writeJSON(w, map[string]any{
			"status_code": 0,
			"data": map[string]any{
				"room": map[string]any{
					"id_str": synthRoomIDLive,
					"status": 2,
					"title":  "合成测试直播间",
					"owner": map[string]any{
						"id_str":   synthUIDUser2,
						"sec_uid":  synthSecUIDUser2,
						"short_id": synthShortID2,
						"web_rid":  synthShortID2,
						"nickname": "创作者二",
					},
				},
			},
		})
	})

	checkNoCookie := func(r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			env.cookieLeakOnMedia.Add(1)
		}
	}

	mux.HandleFunc("/cdn/vod/recent.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		serveBytesWithRange(w, r, "video/mp4", normalVideoMP4)
	})
	mux.HandleFunc("/cdn/vod/livephoto.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		serveBytesWithRange(w, r, "video/mp4", smallClipMP4)
	})
	mux.HandleFunc("/cdn/vod/large.mp4", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		largeVirtual.serveRange(w, r, &env.middleRangeCalls)
	})
	mux.HandleFunc("/cdn/img/", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		serveBytesWithRange(w, r, "image/webp", webpSample)
	})
	mux.HandleFunc("/cdn/audio/bgm.mp3", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		serveBytesWithRange(w, r, "audio/mpeg", mp3Sample)
	})
	mux.HandleFunc("/cdn/live/edge.flv", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		w.Header().Set("Content-Type", "video/x-flv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(liveFLVBytes)
	})
	mux.HandleFunc("/cdn/live/origin.m3u8", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\nseg001.ts?sign=synth\n#EXTINF:4.0,\nseg002.ts?sign=synth\n"))
	})
	mux.HandleFunc("/cdn/live/origin.flv", func(w http.ResponseWriter, r *http.Request) {
		checkNoCookie(r)
		if env.liveFLVHook != nil && env.liveFLVHook(w, r) {
			return
		}
		w.Header().Set("Content-Type", "video/x-flv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(liveFLVBytes)
	})

	writeAct010LocalFiles(t, rootDir, act010Targets{
		Version:    1,
		Act:        "ACT-010",
		ProvidedAt: "2026-11-13",
		Users: []act010User{
			{SecUID: synthSecUIDUser1, URL: "https://www.douyin.com/user/" + synthSecUIDUser1},
			{SecUID: synthSecUIDUser2, URL: "https://www.douyin.com/user/" + synthSecUIDUser2},
		},
		Works: []act010Work{
			{AwemeID: synthAwemePin, Kind: "video", Expect: []string{"pinned"}, URL: "https://www.douyin.com/video/" + synthAwemePin},
			{AwemeID: synthAwemeRecent, Kind: "video", Expect: []string{"recent"}, URL: "https://www.douyin.com/video/" + synthAwemeRecent},
			{AwemeID: synthAwemeNote, Kind: "note", Expect: []string{"recent"}, URL: "https://www.douyin.com/note/" + synthAwemeNote},
			{AwemeID: synthAwemeMixed, Kind: "note", Expect: []string{"mixed"}, URL: "https://www.douyin.com/note/" + synthAwemeMixed},
		},
		LiveRooms: []act010LiveRoom{
			{WebRID: synthShortID2, Form: "short_id", URL: "https://live.douyin.com/" + synthShortID2},
			{WebRID: synthAltWebRID, Form: "other", URL: "https://live.douyin.com/" + synthAltWebRID},
		},
	}, synthCookieRaw)

	return env
}

func mockVideoAwemeJSON(awemeID, secUID, uid string, isTop int, durMs int64, playURL string, dataSize int64) map[string]any {
	return map[string]any{
		"aweme_id":    awemeID,
		"aweme_type":  0,
		"desc":        "合成短视频",
		"create_time": 1700000150,
		"duration":    durMs,
		"is_top":      isTop,
		"author": map[string]any{
			"uid":      uid,
			"sec_uid":  secUID,
			"nickname": "创作者",
		},
		"video": map[string]any{
			"width":      1080,
			"height":     1920,
			"duration":   durMs,
			"is_bytevc1": 0,
			"play_addr": map[string]any{
				"url_list":  []string{playURL},
				"width":     1080,
				"height":    1920,
				"data_size": dataSize,
			},
		},
		"statistics": map[string]any{"digg_count": 99, "play_count": 0},
	}
}

func writeAct010LocalFiles(t *testing.T, rootDir string, tg act010Targets, cookie string) {
	t.Helper()
	siteCheckDir := filepath.Join(rootDir, ".local", "site-check")
	secretDir := filepath.Join(rootDir, ".local", "site-secrets")
	if err := os.MkdirAll(siteCheckDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rawTG, err := json.MarshalIndent(tg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteCheckDir, "targets-douyin.json"), rawTG, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "douyin.cookie"), []byte(cookie+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env.local"), []byte("TRACE_TEST_DOUYIN_COOKIE_FILE=.local/site-secrets/douyin.cookie\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAct010HappyPathLiveAndRedaction(t *testing.T) {
	env := setupAct010MockEnv(t, true)
	outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act010.json")

	rc, err := runAct010(context.Background(), act010Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		RecordDuration: 5 * time.Second,
		MinSpacing:     -1,
		Endpoints:      env.endpoints,
		AllowLocalhost: true,
	})
	if err != nil {
		t.Fatalf("runAct010 failed: %v (steps=%+v)", err, rc.Steps)
	}
	if !rc.Passed || rc.StoppedBy != "" || !rc.TempMediaDeleted || !rc.SecretScanClean {
		t.Fatalf("unexpected receipt state: passed=%v stoppedBy=%q tempDeleted=%v secretClean=%v",
			rc.Passed, rc.StoppedBy, rc.TempMediaDeleted, rc.SecretScanClean)
	}
	if env.cookieLeakOnMedia.Load() != 0 {
		t.Fatalf("Cookie leaked to media/CDN requests %d times", env.cookieLeakOnMedia.Load())
	}
	if env.middleRangeCalls.Load() != 0 {
		t.Fatalf(">24 MiB partial MP4 probe fetched middle chunks (%d calls), want 0", env.middleRangeCalls.Load())
	}

	// Verify V-01..V-08 all populated.
	for _, key := range []string{"V-01", "V-02", "V-03", "V-04", "V-05", "V-06", "V-07", "V-08"} {
		if _, ok := rc.Verifications[key]; !ok {
			t.Fatalf("missing verification %s in receipt", key)
		}
	}

	// Verify V-05 used 2 Range requests on 9 MiB MP4 and validated progressive MP4.
	v05, _ := rc.Verifications["V-05"].(map[string]any)
	if v05["largeFilePartialProbe"] != false || v05["rangeRequests"] != 2 || v05["moovBeforeMdat"] != true {
		t.Fatalf("unexpected V-05 summary: %+v", v05)
	}

	// Verify V-04 mixed-note >24 MiB video page used partial probe (head 8 MiB + tail 1 MiB = 2 requests).
	v04, _ := rc.Verifications["V-04"].(map[string]any)
	mixedVid, _ := v04["mixedNoteVideoPage"].(map[string]any)
	if mixedVid["largeFilePartialProbe"] != true || mixedVid["rangeRequests"] != 2 || mixedVid["hasMoov"] != true {
		t.Fatalf("unexpected V-04 mixedNoteVideoPage partial probe: %+v", mixedVid)
	}

	// Verify V-07 wire video header ("hevc-hvc1") and HLS .ts inspection.
	v07, _ := rc.Verifications["V-07"].(map[string]any)
	if v07["wireVideoTagFormat"] != "hevc-hvc1" || v07["moovBeforeMdat"] != true {
		t.Fatalf("unexpected V-07 summary: %+v", v07)
	}
	hlsMap, _ := v07["hlsPlaylist"].(map[string]any)
	extMap, _ := hlsMap["entryExt"].(map[string]int)
	if extMap[".ts"] != 2 {
		t.Fatalf("expected 2 .ts playlist entries in V-07, got %+v", hlsMap)
	}

	// Verify V-08 two forms comparison.
	v08, _ := rc.Verifications["V-08"].(map[string]any)
	if v08["bothAccepted"] != true || v08["sameOwnerSecUid"] != true || v08["sameRoomId"] != true ||
		v08["shortIdFormMatchesOwnerShortId"] != true || v08["shortIdMatchesProfileOther"] != true {
		t.Fatalf("unexpected V-08 summary: %+v", v08)
	}

	// Verify API usage stayed strictly within ACT-010 limits (14 <= 18).
	if rc.APIUsage.APICalls > act010MaxAPICalls {
		t.Fatalf("API calls=%d exceeded %d", rc.APIUsage.APICalls, act010MaxAPICalls)
	}

	// Read receipt file from disk and verify zero secrets, zero signed URLs, and zero real/synthetic IDs!
	rawBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read act010.json: %v", err)
	}
	rawStr := string(rawBytes)
	if findings := sitecheck.ScanTextForSecrets(rawStr); len(findings) > 0 {
		t.Fatalf("ScanTextForSecrets found leaks in act010.json: %v", findings)
	}
	forbiddenTokens := []string{
		"SENTINEL_SESSION_010", "SENTINEL_SESSION_SS_010", "SENTINEL_GUARD_010",
		"SENTINEL_TTWID_010", "SENTINEL_UIFID_010", "SYNTH_REFRESHED_MSTOKEN_010",
		synthSecUIDSelf, synthSecUIDUser1, synthSecUIDUser2,
		synthUIDSelf, synthUIDUser1, synthUIDUser2,
		synthShortID1, synthShortID2, synthAltWebRID, synthRoomIDLive,
		synthAwemePin, synthAwemeRecent, synthAwemeNote, synthAwemeMixed,
	}
	for _, tok := range forbiddenTokens {
		if strings.Contains(rawStr, tok) {
			t.Fatalf("act010.json leaked forbidden token %q", tok)
		}
	}
}

func TestAct010OfflineLiveRoomMarksV07NotRun(t *testing.T) {
	env := setupAct010MockEnv(t, false)
	outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act010.json")

	rc, err := runAct010(context.Background(), act010Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		RecordDuration: 5 * time.Second,
		MinSpacing:     -1,
		Endpoints:      env.endpoints,
		AllowLocalhost: true,
	})
	if err != nil {
		t.Fatalf("runAct010 offline room failed: %v", err)
	}
	if !rc.Passed {
		t.Fatalf("expected Passed=true when live room is offline")
	}
	if rc.LiveMedia.Requests != 0 || rc.APIUsage.Categories["hls_playlist"] != 0 {
		t.Fatalf("expected 0 live/HLS requests when offline, got live=%d hls=%d",
			rc.LiveMedia.Requests, rc.APIUsage.Categories["hls_playlist"])
	}
	v07, _ := rc.Verifications["V-07"].(map[string]any)
	if v07["status"] != "not-run" {
		t.Fatalf("expected V-07 status=not-run, got %+v", v07)
	}
}

func TestAct010PreFlightQuotasAndPacing(t *testing.T) {
	t.Run("second run refused with 0 HTTP requests", func(t *testing.T) {
		env := setupAct010MockEnv(t, false)
		if _, err := runAct010(context.Background(), act010Config{
			RootDir: env.rootDir, RecordDuration: 5 * time.Second, MinSpacing: -1,
			Endpoints: env.endpoints, AllowLocalhost: true,
		}); err != nil {
			t.Fatalf("first run failed: %v", err)
		}
		before := env.totalRequests.Load()

		_, err := runAct010(context.Background(), act010Config{
			RootDir: env.rootDir, RecordDuration: 5 * time.Second, MinSpacing: -1,
			Endpoints: env.endpoints, AllowLocalhost: true,
		})
		if !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("expected ErrQuotaExceeded on 2nd run, got: %v", err)
		}
		if got := env.totalRequests.Load() - before; got != 0 {
			t.Fatalf("2nd run sent %d HTTP requests, want 0", got)
		}
	})

	t.Run("record duration over 25s refused with 0 HTTP requests", func(t *testing.T) {
		env := setupAct010MockEnv(t, false)
		_, err := runAct010(context.Background(), act010Config{
			RootDir: env.rootDir, RecordDuration: 26 * time.Second, MinSpacing: -1,
			Endpoints: env.endpoints, AllowLocalhost: true,
		})
		if err == nil {
			t.Fatal("expected error for RecordDuration > 25s")
		}
		if env.totalRequests.Load() != 0 {
			t.Fatalf("sent %d requests, want 0", env.totalRequests.Load())
		}
	})

	t.Run("short link in targets refused pre-flight with 0 HTTP requests", func(t *testing.T) {
		env := setupAct010MockEnv(t, false)
		writeAct010LocalFiles(t, env.rootDir, act010Targets{
			Version: 1, Act: "ACT-010",
			Users:     []act010User{{SecUID: synthSecUIDUser1, URL: "https://v.douyin.com/iSynthShort/"}},
			Works:     []act010Work{{AwemeID: synthAwemeRecent, Kind: "video"}},
			LiveRooms: []act010LiveRoom{{WebRID: synthShortID2, Form: "short_id"}},
		}, synthCookieRaw)

		_, err := runAct010(context.Background(), act010Config{
			RootDir: env.rootDir, RecordDuration: 5 * time.Second, MinSpacing: -1,
			Endpoints: env.endpoints, AllowLocalhost: true,
		})
		if !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("expected ErrQuotaExceeded on short-link target, got: %v", err)
		}
		if env.totalRequests.Load() != 0 {
			t.Fatalf("sent %d requests, want 0", env.totalRequests.Load())
		}
	})

	t.Run("category cap and unlisted/shortlink transport refused before RoundTrip", func(t *testing.T) {
		ledgerDir := t.TempDir()
		gate, err := sitecheck.OpenGate(ledgerDir, sitecheck.Limits{
			ActID: act010ID, MaxRuns: 1, MaxAPICalls: act010MaxAPICalls, MaxByCategory: act010APILimits,
		})
		if err != nil {
			t.Fatal(err)
		}
		var upstreamCalls atomic.Int64
		ck := &act010Checker{
			gate:           gate,
			vod:            &mediaBudget{name: "vod", maxRequests: 1, maxBytes: 16},
			live:           &mediaBudget{name: "live", maxRequests: 1, maxBytes: 16},
			statuses:       make(map[string]int),
			apiStatusCodes: make(map[string]map[int]int),
		}
		rt := ck.wrap(roundTrip(func(r *http.Request) (*http.Response, error) {
			upstreamCalls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       http.NoBody,
			}, nil
		}))

		// 1. Short-link host refused before RoundTrip.
		reqShort, _ := http.NewRequest(http.MethodGet, "https://v.douyin.com/iSynth/", nil)
		if _, err := rt.RoundTrip(reqShort); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("expected ErrQuotaExceeded on v.douyin.com, got %v", err)
		}
		// 2. Unlisted endpoint refused before RoundTrip.
		reqUnlisted, _ := http.NewRequest(http.MethodGet, "https://webcast.amemv.com/webcast/room/check_alive/", nil)
		if _, err := rt.RoundTrip(reqUnlisted); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("expected ErrQuotaExceeded on check_alive, got %v", err)
		}
		if upstreamCalls.Load() != 0 {
			t.Fatalf("upstream called %d times on forbidden endpoints, want 0", upstreamCalls.Load())
		}

		// 3. Exhaust reflow_info limit (2), verify 3rd is refused BEFORE RoundTrip!
		for i := 0; i < 2; i++ {
			reqReflow, _ := http.NewRequest(http.MethodGet, "https://webcast.amemv.com/webcast/room/reflow/info/", nil)
			// Reset stop reason caused by empty NoBody so we test pure quota cap.
			ck.mu.Lock()
			ck.stopped = ""
			ck.mu.Unlock()
			_, _ = rt.RoundTrip(reqReflow)
		}
		ck.mu.Lock()
		ck.stopped = ""
		ck.mu.Unlock()
		before := upstreamCalls.Load()
		reqOver, _ := http.NewRequest(http.MethodGet, "https://webcast.amemv.com/webcast/room/reflow/info/", nil)
		if _, err := rt.RoundTrip(reqOver); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("expected ErrQuotaExceeded on 3rd reflow_info, got %v", err)
		}
		if upstreamCalls.Load() != before {
			t.Fatal("over-quota request reached upstream RoundTrip")
		}

		// 4. VOD request cap (1) refuses 2nd request before RoundTrip.
		ck.vodMediaActive.Store(true)
		reqVOD1, _ := http.NewRequest(http.MethodGet, "https://v3-web.douyinvod.com/clip.mp4", nil)
		_, _ = rt.RoundTrip(reqVOD1)
		beforeVOD := upstreamCalls.Load()
		reqVOD2, _ := http.NewRequest(http.MethodGet, "https://v3-web.douyinvod.com/clip2.mp4", nil)
		if _, err := rt.RoundTrip(reqVOD2); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("expected ErrQuotaExceeded on 2nd VOD request, got %v", err)
		}
		if upstreamCalls.Load() != beforeVOD {
			t.Fatal("over-quota VOD request reached upstream RoundTrip")
		}
	})

	t.Run("serial minSpacing enforced across requests", func(t *testing.T) {
		env := setupAct010MockEnv(t, false)
		// Trim targets to minimal counts so spacing check finishes in ~300ms.
		writeAct010LocalFiles(t, env.rootDir, act010Targets{
			Version: 1, Act: "ACT-010",
			Users:     []act010User{{SecUID: synthSecUIDUser1}},
			Works:     []act010Work{{AwemeID: synthAwemeNote, Kind: "note"}},
			LiveRooms: []act010LiveRoom{{WebRID: synthShortID2, Form: "short_id"}},
		}, synthCookieRaw)

		if _, err := runAct010(context.Background(), act010Config{
			RootDir: env.rootDir, RecordDuration: 5 * time.Second,
			MinSpacing: 30 * time.Millisecond, Endpoints: env.endpoints, AllowLocalhost: true,
		}); err != nil {
			t.Fatalf("runAct010 with spacing failed: %v", err)
		}
		env.mu.Lock()
		times := append([]time.Time(nil), env.reqTimes...)
		env.mu.Unlock()
		if len(times) < 4 {
			t.Fatalf("expected >= 4 requests, got %d", len(times))
		}
		for i := 1; i < len(times); i++ {
			if gap := times[i].Sub(times[i-1]); gap < 22*time.Millisecond {
				t.Fatalf("request gap[%d]=%s < expected 30ms spacing", i, gap)
			}
		}
	})
}

func TestAct010ZeroToleranceStopConditions(t *testing.T) {
	cases := []struct {
		name        string
		hook        func(w http.ResponseWriter, r *http.Request) bool
		wantStopped string
	}{
		{
			name: "http 403 ArgusSecurityPlugin",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("Blocked by ArgusSecurityPlugin"))
				return true
			},
			wantStopped: "http_403_argus",
		},
		{
			name: "http 429 rate limited",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"status_code":429}`))
				return true
			},
			wantStopped: "http_429",
		},
		{
			name: "http 444 gateway drop",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.WriteHeader(444)
				return true
			},
			wantStopped: "http_444",
		},
		{
			name: "http 200 zero-byte body",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				return true
			},
			wantStopped: "empty_api_body",
		},
		{
			name: "http 200 HTML verification page",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("<html><head><title>验证码中间页</title></head><body>verify</body></html>"))
				return true
			},
			wantStopped: "html_verify_page",
		},
		{
			name: "http 200 non-empty verify_center_decision_conf",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status_code":0,"verify_center_decision_conf":"{\"subtype\":\"slide\"}"}`))
				return true
			},
			wantStopped: "json_verify_challenge",
		},
		{
			name: "authenticated request returns unauthenticated status_code 2483",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status_code":2483,"status_msg":"未登录"}`))
				return true
			},
			wantStopped: "auth_required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupAct010MockEnv(t, true)
			env.profileOtherHook = tc.hook
			outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act010.json")

			rc, err := runAct010(context.Background(), act010Config{
				RootDir:        env.rootDir,
				OutPath:        outPath,
				RecordDuration: 5 * time.Second,
				MinSpacing:     -1,
				Endpoints:      env.endpoints,
				AllowLocalhost: true,
			})
			if err == nil {
				t.Fatalf("expected runAct010 to fail on %s, got nil", tc.name)
			}
			if rc.Passed {
				t.Fatalf("expected Passed=false on %s", tc.name)
			}
			if rc.StoppedBy != tc.wantStopped {
				t.Fatalf("StoppedBy=%q, want %q", rc.StoppedBy, tc.wantStopped)
			}
			// Exact request count must be 3:
			// 1 (guest profile/self) + 1 (auth profile/self) + 1 (failing profile/other), 0 retries, 0 subsequent calls!
			if got := env.totalRequests.Load(); got != 3 {
				t.Fatalf("total HTTP requests=%d, want exact 3 (0 retries, 0 subsequent calls)", got)
			}
			if !rc.TempMediaDeleted || !rc.SecretScanClean {
				t.Fatalf("expected TempMediaDeleted=true & SecretScanClean=true on stop receipt")
			}
		})
	}
}

func TestAct010RedactorBrowserCookieLiteralsAndHandles(t *testing.T) {
	red := newAct010Redactor()
	red.addCookieHeader(synthCookieRaw + "; theme=false; passport_assist_user=true")
	red.addHandles("video", "creator_custom_handle")

	rc := act010Receipt{
		Act:             "ACT-010",
		StartedAt:       "2026-11-12T20:00:00Z",
		FinishedAt:      "2026-11-12T20:01:00Z",
		Passed:          false,
		SecretScanClean: true,
		Steps: []step{
			{
				Name:    "v02_v04_works_detail",
				OK:      true,
				Skipped: false,
				Facts:   map[string]any{"declaredKind": "video", "videoWidth": 1920, "videoHeight": 1080},
			},
		},
	}
	rawJSON, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := red.verifyJSONClean(string(rawJSON), &rc); err != nil {
		t.Fatalf("expected clean receipt despite is_staff_user=false / dy_swidth=1920 / handle='video', got: %v", err)
	}

	// Leaking a stripped bd_ticket_guard_* cookie value or unique_id in step error MUST be rejected.
	if err := red.verifyJSONClean(string(rawJSON)+" SENTINEL_TICKET_GUARD_010", &rc); err == nil {
		t.Fatal("expected verifyJSONClean to reject leaked bd_ticket_guard_client_data value")
	}
	rcLeak := rc
	rcLeak.Steps = []step{{Name: "fail", Error: "failed for creator_custom_handle"}}
	rawLeak, _ := json.MarshalIndent(rcLeak, "", "  ")
	if err := red.verifyJSONClean(string(rawLeak), &rcLeak); err == nil {
		t.Fatal("expected verifyJSONClean to reject unredacted account unique_id in step error")
	}
}
