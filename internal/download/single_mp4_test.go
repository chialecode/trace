package download

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

// buildProgressiveWithoutElst builds a clean progressive MP4 (video + optional audio)
// with all edts/elst boxes stripped, either in moov-first or mdat-before-moov order.
func buildProgressiveWithoutElst(t *testing.T, withAudio bool, mdatBeforeMoov bool, extraMdatPad int) []byte {
	t.Helper()
	dir := t.TempDir()
	vBytes, err := BuildSyntheticVideoM4S(60, 30) // 2.0s @ 30fps
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	vPath := filepath.Join(dir, "v.m4s")
	if err := os.WriteFile(vPath, vBytes, 0600); err != nil {
		t.Fatalf("write v.m4s: %v", err)
	}

	aPath := ""
	if withAudio {
		aBytes, err := BuildSyntheticAudioM4S(94) // ~2.005s @ 48kHz
		if err != nil {
			t.Fatalf("BuildSyntheticAudioM4S: %v", err)
		}
		aPath = filepath.Join(dir, "a.m4s")
		if err := os.WriteFile(aPath, aBytes, 0600); err != nil {
			t.Fatalf("write a.m4s: %v", err)
		}
	}

	muxOut := filepath.Join(dir, "prog.mp4")
	if _, err := MuxDASHFiles(vPath, aPath, muxOut); err != nil {
		t.Fatalf("MuxDASHFiles: %v", err)
	}
	rawProg, err := os.ReadFile(muxOut)
	if err != nil {
		t.Fatalf("ReadFile prog.mp4: %v", err)
	}

	parsed, err := mp4.DecodeFile(bytes.NewReader(rawProg))
	if err != nil {
		t.Fatalf("DecodeFile prog.mp4: %v", err)
	}

	// Strip edts/elst from every track and downgrade co64 -> stco so normal progressive
	// camera/CDN MP4 layout (stco, no elst) is exercised directly.
	origMoovSize := parsed.Moov.Size()
	for _, trak := range parsed.Moov.Traks {
		if trak.Edts != nil {
			var filtered []mp4.Box
			for _, ch := range trak.Children {
				if ch.Type() != "edts" {
					filtered = append(filtered, ch)
				}
			}
			trak.Children = filtered
			trak.Edts = nil
		}
		stbl := trak.Mdia.Minf.Stbl
		if stbl.Co64 != nil {
			stco := &mp4.StcoBox{
				Version:     stbl.Co64.Version,
				Flags:       stbl.Co64.Flags,
				ChunkOffset: make([]uint32, len(stbl.Co64.ChunkOffset)),
			}
			for i, off := range stbl.Co64.ChunkOffset {
				stco.ChunkOffset[i] = uint32(off)
			}
			for i, ch := range stbl.Children {
				if ch == stbl.Co64 {
					stbl.Children[i] = stco
				}
			}
			stbl.Co64 = nil
			stbl.Stco = stco
		}
	}

	if extraMdatPad > 0 {
		pad := bytes.Repeat([]byte{0x5A}, extraMdatPad)
		parsed.Mdat.AddSampleData(pad)
	}

	if !mdatBeforeMoov {
		newMoovSize := parsed.Moov.Size()
		delta := int64(newMoovSize) - int64(origMoovSize)
		for _, trak := range parsed.Moov.Traks {
			stco := trak.Mdia.Minf.Stbl.Stco
			for i := range stco.ChunkOffset {
				stco.ChunkOffset[i] = uint32(int64(stco.ChunkOffset[i]) + delta)
			}
		}
		var out bytes.Buffer
		if err := parsed.Encode(&out); err != nil {
			t.Fatalf("Encode moov-first: %v", err)
		}
		return out.Bytes()
	}

	// Build [ftyp, free(64B), mdat, moov] layout:
	// In the original [ftyp, moov, mdat], mdat payload started at ftyp.Size() + origMoovSize + mdat.HeaderSize().
	// In [ftyp, free, mdat, moov], mdat payload starts at ftyp.Size() + freeBox.Size() + mdat.HeaderSize().
	freeBox := mp4.NewFreeBox(make([]byte, 56)) // 64-byte free box
	oldMdatPayloadStart := int64(parsed.Ftyp.Size() + origMoovSize + parsed.Mdat.HeaderSize())
	newMdatPayloadStart := int64(parsed.Ftyp.Size() + freeBox.Size() + parsed.Mdat.HeaderSize())
	delta := newMdatPayloadStart - oldMdatPayloadStart

	for _, trak := range parsed.Moov.Traks {
		stco := trak.Mdia.Minf.Stbl.Stco
		for i := range stco.ChunkOffset {
			stco.ChunkOffset[i] = uint32(int64(stco.ChunkOffset[i]) + delta)
		}
	}

	var out bytes.Buffer
	for _, box := range []mp4.Box{parsed.Ftyp, freeBox, parsed.Mdat, parsed.Moov} {
		if err := box.Encode(&out); err != nil {
			t.Fatalf("Encode mdat-before-moov box %s: %v", box.Type(), err)
		}
	}
	return out.Bytes()
}

func extractAllTrackChunkPayloads(t *testing.T, raw []byte) [][][]byte {
	t.Helper()
	parsed, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("DecodeFile for sample verify: %v", err)
	}
	var allTracks [][][]byte
	for _, trak := range parsed.Moov.Traks {
		stbl := trak.Mdia.Minf.Stbl
		var offsets []uint64
		if stbl.Stco != nil {
			for _, off := range stbl.Stco.ChunkOffset {
				offsets = append(offsets, uint64(off))
			}
		} else if stbl.Co64 != nil {
			offsets = append(offsets, stbl.Co64.ChunkOffset...)
		}

		// Expand stsc + stsz to read every sample byte from raw at its exact chunk offset.
		var trackSamples [][]byte
		sampleNr := uint32(1)
		totalSamples := stbl.Stsz.SampleNumber
		for chunkIdx, chunkOff := range offsets {
			chunkNr := int(chunkIdx + 1)
			samplesInChunk := uint32(0)
			for i, entry := range stbl.Stsc.Entries {
				if int(entry.FirstChunk) <= chunkNr && (i+1 == len(stbl.Stsc.Entries) || chunkNr < int(stbl.Stsc.Entries[i+1].FirstChunk)) {
					samplesInChunk = entry.SamplesPerChunk
					break
				}
			}
			curOff := chunkOff
			for s := uint32(0); s < samplesInChunk && sampleNr <= totalSamples; s++ {
				sz := stbl.Stsz.GetSampleSize(int(sampleNr))
				if curOff+uint64(sz) > uint64(len(raw)) {
					t.Fatalf("track %d sample %d range [%d, %d) exceeds file len %d", trak.Tkhd.TrackID, sampleNr, curOff, curOff+uint64(sz), len(raw))
				}
				cp := make([]byte, sz)
				copy(cp, raw[curOff:curOff+uint64(sz)])
				trackSamples = append(trackSamples, cp)
				curOff += uint64(sz)
				sampleNr++
			}
		}
		allTracks = append(allTracks, trackSamples)
	}
	return allTracks
}

func TestSingleMP4RelocateMoovBeforeMdatWithoutElstAndVerifySamples(t *testing.T) {
	origBytes := buildProgressiveWithoutElst(t, true, true, 0)
	origSamples := extractAllTrackChunkPayloads(t, origBytes)

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(videoPath, origBytes, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Verify initial state really has moov AFTER mdat and no elst.
	beforeSummary, err := InspectMP4File(videoPath)
	if err != nil {
		t.Fatalf("InspectMP4File before: %v", err)
	}
	if beforeSummary.MoovBeforeMdat || beforeSummary.HasElst {
		t.Fatalf("expected initial mdat-before-moov without elst, got %+v", beforeSummary)
	}

	// Expected duration 3.5s is within <= 2.0s tolerance of ~2.0s actual duration.
	summary, err := EnsureSingleMP4(videoPath, 3.5)
	if err != nil {
		t.Fatalf("EnsureSingleMP4 failed: %v", err)
	}
	if !summary.MoovBeforeMdat {
		t.Fatalf("expected MoovBeforeMdat=true after relocation, got %+v", summary)
	}
	if summary.HasElst {
		t.Fatalf("single-file MP4 relocation must not require or invent elst, got HasElst=true")
	}
	if summary.TrackCount != 2 || summary.Width != 640 || summary.Height != 360 || summary.SHA256 == "" {
		t.Fatalf("unexpected relocated summary: %+v", summary)
	}

	afterBytes, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	afterSamples := extractAllTrackChunkPayloads(t, afterBytes)
	if len(afterSamples) != len(origSamples) {
		t.Fatalf("track count mismatch: got %d want %d", len(afterSamples), len(origSamples))
	}
	for ti := range origSamples {
		if len(afterSamples[ti]) != len(origSamples[ti]) {
			t.Fatalf("track %d sample count mismatch: got %d want %d", ti, len(afterSamples[ti]), len(origSamples[ti]))
		}
		for si := range origSamples[ti] {
			if !bytes.Equal(afterSamples[ti][si], origSamples[ti][si]) {
				t.Fatalf("track %d sample %d payload corrupted after moov relocation", ti, si)
			}
		}
	}
}

func TestSingleMP4BoundedMemoryRelocationAndFailurePreservesOriginal(t *testing.T) {
	// Create an ~16 MiB mdat-before-moov MP4 on disk and verify relocation streams on disk
	// without allocating an in-memory copy of mdat.
	const padBytes = 16 * 1024 * 1024
	dir := t.TempDir()
	path := filepath.Join(dir, "large.mp4")
	orig := buildProgressiveWithoutElst(t, false, true, padBytes)
	if err := os.WriteFile(path, orig, 0600); err != nil {
		t.Fatalf("WriteFile large.mp4: %v", err)
	}
	origLen := len(orig)
	origPrefix := append([]byte(nil), orig[:128]...)
	orig = nil // Release test builder slice before measuring heap live bytes.

	runtime.GC()
	var beforeMem runtime.MemStats
	runtime.ReadMemStats(&beforeMem)

	summary, err := EnsureSingleMP4(path, 2.0)
	if err != nil {
		t.Fatalf("EnsureSingleMP4 large.mp4: %v", err)
	}
	if !summary.MoovBeforeMdat || summary.FileBytes != int64(origLen) {
		t.Fatalf("unexpected large.mp4 summary: %+v", summary)
	}

	runtime.GC()
	var afterMem runtime.MemStats
	runtime.ReadMemStats(&afterMem)
	var liveDelta int64
	if afterMem.HeapAlloc > beforeMem.HeapAlloc {
		liveDelta = int64(afterMem.HeapAlloc - beforeMem.HeapAlloc)
	}
	if liveDelta > 4*1024*1024 {
		t.Fatalf("expected bounded live heap delta << 16 MiB mdat, got %d bytes", liveDelta)
	}

	// Verify canceled context failure preserves original file untouched and leaves zero temp files.
	failDir := t.TempDir()
	failPath := filepath.Join(failDir, "cancel.mp4")
	cancelInput := buildProgressiveWithoutElst(t, true, true, 0)
	if err := os.WriteFile(failPath, cancelInput, 0600); err != nil {
		t.Fatalf("WriteFile cancel.mp4: %v", err)
	}
	_ = origPrefix

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EnsureSingleMP4Context(ctx, failPath, 2.0); err == nil {
		t.Fatalf("expected canceled EnsureSingleMP4Context to fail")
	}
	preserved, err := os.ReadFile(failPath)
	if err != nil || !bytes.Equal(preserved, cancelInput) {
		t.Fatalf("original file was not preserved byte-for-byte after failure")
	}
	entries, _ := os.ReadDir(failDir)
	if len(entries) != 1 {
		t.Fatalf("expected no leftover temp files in %s, got %d entries", failDir, len(entries))
	}
}

func TestSingleMP4StcoToCo64UpgradeNear4GiBBoundary(t *testing.T) {
	// Exercise relocateMoovOnDisk's >4 GiB stco -> co64 upgrade path using a sparse file
	// on disk without hashing 4 GiB.
	dir := t.TempDir()
	sparsePath := filepath.Join(dir, "sparse4g.mp4")
	f, err := os.OpenFile(sparsePath, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
	if err != nil {
		t.Fatalf("create sparse file: %v", err)
	}
	defer f.Close()

	// Build a minimal parsed *mp4.File whose mdat starts at offset 24 with 64-bit size ~4 GiB,
	// and a stco chunk offset at 0xFFFFFFF0 so adding moov.Size() overflows uint32 -> upgrades to co64!
	ftyp := mp4.CreateFtyp()
	var ftypBuf bytes.Buffer
	_ = ftyp.Encode(&ftypBuf)
	if _, err := f.Write(ftypBuf.Bytes()); err != nil {
		t.Fatalf("write ftyp: %v", err)
	}

	// Write 16-byte 64-bit mdat header right after ftyp, with only 64 bytes of actual lazy payload
	// needed if we test offset shifting, OR test the exact stco->co64 shift on a small mdat whose
	// chunk offset sits near 0xFFFFFFF0? Wait: chunk offset must be < origMoovStart!
	// Let's build a synthetic mp4.File in memory with lazy mdat size = 0xFFFFFFF8 and verify
	// stco -> co64 upgrade directly on the moov box!
	base := buildProgressiveWithoutElst(t, false, true, 0)
	parsed, err := mp4.DecodeFile(bytes.NewReader(base), mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	var mdatIdx, moovIdx int
	for i, ch := range parsed.Children {
		if ch.Type() == "mdat" {
			mdatIdx = i
		}
		if ch.Type() == "moov" {
			moovIdx = i
		}
	}
	mdat := parsed.Children[mdatIdx].(*mp4.MdatBox)
	// Simulate an mdat ending at 0x100000020 with a stco chunk at 0xFFFFFFF0 (within uint32 before relocation,
	// > uint32 after adding moov.Size()!).
	parsed.Moov.StartPos = 0x100000020
	stbl := parsed.Moov.Traks[0].Mdia.Minf.Stbl
	origChunkOff := uint32(0xFFFFFFF0)
	stbl.Stco.ChunkOffset = []uint32{origChunkOff}

	// Run the exact relocation offset calculator via a 0-byte lazy mdat so disk I/O stays tiny!
	mdat.SetLazyDataSize(0)
	_ = mdatIdx
	_ = moovIdx

	// Write base bytes into sparsePath and invoke relocateMoovOnDisk directly.
	_ = f.Close()
	if err := os.WriteFile(sparsePath, base, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	src, err := os.Open(sparsePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	srcClosed := false
	defer func() {
		if !srcClosed {
			_ = src.Close()
		}
	}()

	if err := relocateMoovOnDisk(context.Background(), src, &srcClosed, parsed, moovIdx, mdatIdx, sparsePath); err != nil {
		t.Fatalf("relocateMoovOnDisk co64 upgrade: %v", err)
	}
	if stbl.Stco != nil || stbl.Co64 == nil || len(stbl.Co64.ChunkOffset) != 1 {
		t.Fatalf("expected stco to be upgraded to co64, got stco=%v co64=%v", stbl.Stco, stbl.Co64)
	}
	expectedShifted := uint64(origChunkOff) + parsed.Moov.Size()
	if stbl.Co64.ChunkOffset[0] != expectedShifted {
		t.Fatalf("co64 offset = %d, want %d", stbl.Co64.ChunkOffset[0], expectedShifted)
	}
}

func TestSingleMP4MoovFirstFastPathDoesNotRewriteFile(t *testing.T) {
	// Video-only progressive MP4 (e.g. Douyin live-photo motion clip) with moov already before mdat and no elst.
	moovFirst := buildProgressiveWithoutElst(t, false, false, 0)
	path := filepath.Join(t.TempDir(), "01_live.mp4")
	if err := os.WriteFile(path, moovFirst, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	sentinelTime := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, sentinelTime, sentinelTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	summary, err := EnsureSingleMP4(path, 2.0)
	if err != nil {
		t.Fatalf("EnsureSingleMP4 fast path: %v", err)
	}
	if !summary.MoovBeforeMdat || summary.HasElst || summary.TrackCount != 1 {
		t.Fatalf("unexpected fast-path summary: %+v", summary)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.ModTime().Equal(sentinelTime) {
		t.Fatalf("expected moov-first fast path not to rewrite file, mtime changed from %v to %v", sentinelTime, info.ModTime())
	}
}

func TestSingleMP4RejectsNoVideoDurationDiffAndEncryptedCENC(t *testing.T) {
	// 1. Duration mismatch > 2s rejected.
	t.Run("DurationDiffOver2sRejected", func(t *testing.T) {
		raw := buildProgressiveWithoutElst(t, true, false, 0) // ~2.0s actual
		path := filepath.Join(t.TempDir(), "dur-mismatch.mp4")
		_ = os.WriteFile(path, raw, 0600)
		_, err := EnsureSingleMP4(path, 4.5) // |2.0 - 4.5| = 2.5s > 2.0s
		if err == nil || !IsSingleMP4ValidationError(err) {
			t.Fatalf("expected SingleMP4ValidationError for >2s duration diff, got %v", err)
		}
	})

	// 2. Audio-only MP4 (no video track) rejected.
	t.Run("NoVideoTrackRejected", func(t *testing.T) {
		aM4S, err := BuildSyntheticAudioM4S(90)
		if err != nil {
			t.Fatalf("BuildSyntheticAudioM4S: %v", err)
		}
		dir := t.TempDir()
		audioOnlyPath := filepath.Join(dir, "audio-only.mp4")
		if _, err := DefragmentFMP4Bytes(aM4S, audioOnlyPath); err != nil {
			t.Fatalf("DefragmentFMP4Bytes: %v", err)
		}
		_, err = EnsureSingleMP4(audioOnlyPath, 0)
		if err == nil || !IsSingleMP4ValidationError(err) {
			t.Fatalf("expected SingleMP4ValidationError for MP4 without video track, got %v", err)
		}
	})

	// 3. CENC encrypted encv sample entry rejected with EncryptedMediaError.
	t.Run("EncryptedEncvRejected", func(t *testing.T) {
		raw := buildProgressiveWithoutElst(t, true, false, 0)
		// Replace sample entry FourCC "avc1" with CENC "encv" in-place (same box length!).
		encRaw := bytes.Replace(raw, []byte("avc1"), []byte("encv"), 1)
		if bytes.Equal(encRaw, raw) {
			t.Fatalf("failed to inject encv box into synthetic MP4")
		}
		path := filepath.Join(t.TempDir(), "cenc-encv.mp4")
		_ = os.WriteFile(path, encRaw, 0600)
		_, err := EnsureSingleMP4(path, 2.0)
		if !IsEncryptedMedia(err) {
			t.Fatalf("expected EncryptedMediaError for encv CENC track, got %v", err)
		}
	})

	// 4. CENC encrypted enca audio sample entry rejected with EncryptedMediaError.
	t.Run("EncryptedEncaRejected", func(t *testing.T) {
		raw := buildProgressiveWithoutElst(t, true, false, 0)
		encRaw := bytes.Replace(raw, []byte("mp4a"), []byte("enca"), 1)
		if bytes.Equal(encRaw, raw) {
			t.Fatalf("failed to inject enca box into synthetic MP4")
		}
		path := filepath.Join(t.TempDir(), "cenc-enca.mp4")
		_ = os.WriteFile(path, encRaw, 0600)
		_, err := EnsureSingleMP4(path, 2.0)
		if !IsEncryptedMedia(err) {
			t.Fatalf("expected EncryptedMediaError for enca CENC track, got %v", err)
		}
	})
}
