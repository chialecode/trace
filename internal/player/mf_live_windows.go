//go:build windows

package player

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procMFCreateAsyncResult                = modMFPlat.NewProc("MFCreateAsyncResult")
	procMFCreateSourceReaderFromByteStream = modMFReadWrite.NewProc("MFCreateSourceReaderFromByteStream")
)

const (
	mfByteStreamIsReadable            = 0x00000001
	mfByteStreamIsSeekable            = 0x00000004
	mfByteStreamHasSlowSeek           = 0x00000100
	mfByteStreamIsPartiallyDownloaded = 0x00000200

	hrOK          = uintptr(0)
	hrENoIface    = uintptr(0x80004002)
	hrENotImpl    = uintptr(0x80004001)
	hrEPointer    = uintptr(0x80004003)
	hrEAbort      = uintptr(0x80004004)
	hrEFail       = uintptr(0x80004005)
	hrEInvalidArg = uintptr(0x80070057)
)

var (
	// IID_IUnknown {00000000-0000-0000-C000-000000000046}
	iidIUnknown = windows.GUID{
		Data1: 0x00000000, Data2: 0x0000, Data3: 0x0000,
		Data4: [8]byte{0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
	}
	// IID_IMFByteStream {ad4c1b00-4bf7-422f-9175-756693d9130d}
	iidIMFByteStream = windows.GUID{
		Data1: 0xad4c1b00, Data2: 0x4bf7, Data3: 0x422f,
		Data4: [8]byte{0x91, 0x75, 0x75, 0x66, 0x93, 0xd9, 0x13, 0x0d},
	}
	// IID_IMFAttributes {2cd2d921-c447-44a7-a13c-4adabfc247e3}
	iidIMFAttributes = windows.GUID{
		Data1: 0x2cd2d921, Data2: 0xc447, Data3: 0x44a7,
		Data4: [8]byte{0xa1, 0x3c, 0x4a, 0xda, 0xbf, 0xc2, 0x47, 0xe3},
	}
	// MF_BYTESTREAM_CONTENT_TYPE {fc358289-3cb6-460c-a424-b6681260375a}
	guidMFByteStreamContentType = windows.GUID{
		Data1: 0xfc358289, Data2: 0x3cb6, Data3: 0x460c,
		Data4: [8]byte{0xa4, 0x24, 0xb6, 0x68, 0x12, 0x60, 0x37, 0x5a},
	}
	// MF_BYTESTREAM_ORIGIN_NAME {fc358288-3cb6-460c-a424-b6681260375a}
	guidMFByteStreamOriginName = windows.GUID{
		Data1: 0xfc358288, Data2: 0x3cb6, Data3: 0x460c,
		Data4: [8]byte{0xa4, 0x24, 0xb6, 0x68, 0x12, 0x60, 0x37, 0x5a},
	}
)

func openSharedReadFile(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

type mfByteStreamVtbl struct {
	QueryInterface     uintptr
	AddRef             uintptr
	Release            uintptr
	GetCapabilities    uintptr
	GetLength          uintptr
	SetLength          uintptr
	GetCurrentPosition uintptr
	SetCurrentPosition uintptr
	IsEndOfStream      uintptr
	Read               uintptr
	BeginRead          uintptr
	EndRead            uintptr
	Write              uintptr
	BeginWrite         uintptr
	EndWrite           uintptr
	Seek               uintptr
	Flush              uintptr
	Close              uintptr
}

type mfByteStreamCOMHeader struct {
	lpVtbl *mfByteStreamVtbl
}

type mfVirtSpan struct {
	virtOff int64
	length  int64
	isPad   bool
	fileOff int64
}

type mfAsyncReadOutcome struct {
	n  uint32
	hr uintptr
}

type mfLiveByteStream struct {
	com           mfByteStreamCOMHeader
	comAddr       uintptr
	refCount      atomic.Int32
	ownerReleased atomic.Bool
	attrs         uintptr
	mfStarted     bool

	mu             sync.Mutex
	cond           *sync.Cond
	file           *os.File
	spans          []mfVirtSpan
	virtEnd        int64
	realFileCursor int64
	hasUnpadded    bool
	pos            int64
	eof            bool
	closed         bool
	pendingReads   map[uintptr]mfAsyncReadOutcome

	wg        sync.WaitGroup
	cleanOnce sync.Once
}

var (
	mfLiveVtblOnce sync.Once
	mfLiveVtbl     mfByteStreamVtbl

	mfLiveStreamMapMu sync.RWMutex
	mfLiveStreamMap   = make(map[uintptr]*mfLiveByteStream)
)

func lookupMFLiveByteStream(this uintptr) *mfLiveByteStream {
	mfLiveStreamMapMu.RLock()
	bs := mfLiveStreamMap[this]
	mfLiveStreamMapMu.RUnlock()
	return bs
}

func initMFLiveByteStreamVtbl() {
	mfLiveVtblOnce.Do(func() {
		mfLiveVtbl = mfByteStreamVtbl{
			QueryInterface: syscall.NewCallback(func(this uintptr, riid *windows.GUID, ppv *uintptr) uintptr {
				if ppv == nil {
					return hrEPointer
				}
				*ppv = 0
				if riid == nil {
					return hrEPointer
				}
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					return hrENoIface
				}
				if *riid == iidIUnknown || *riid == iidIMFByteStream {
					bs.refCount.Add(1)
					*ppv = this
					return hrOK
				}
				if *riid == iidIMFAttributes {
					bs.mu.Lock()
					attrs := bs.attrs
					if attrs != 0 && !bs.closed {
						_ = vcall(attrs, 1) // AddRef while locked so finalize cannot release concurrently
						*ppv = attrs
						bs.mu.Unlock()
						return hrOK
					}
					bs.mu.Unlock()
					return hrENoIface
				}
				return hrENoIface
			}),
			AddRef: syscall.NewCallback(func(this uintptr) uintptr {
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					return 0
				}
				return uintptr(bs.refCount.Add(1))
			}),
			Release: syscall.NewCallback(func(this uintptr) uintptr {
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					return 0
				}
				return uintptr(bs.releaseRef())
			}),
			GetCapabilities: syscall.NewCallback(func(this uintptr, pdwCaps *uint32) uintptr {
				if pdwCaps == nil {
					return hrEPointer
				}
				*pdwCaps = mfByteStreamIsReadable | mfByteStreamIsSeekable | mfByteStreamHasSlowSeek | mfByteStreamIsPartiallyDownloaded
				return hrOK
			}),
			GetLength: syscall.NewCallback(func(this uintptr, pqwLen *uint64) uintptr {
				if pqwLen == nil {
					return hrEPointer
				}
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					return hrEFail
				}
				bs.mu.Lock()
				if bs.eof || bs.closed {
					*pqwLen = uint64(bs.virtEnd)
				} else {
					*pqwLen = ^uint64(0)
				}
				bs.mu.Unlock()
				return hrOK
			}),
			SetLength: syscall.NewCallback(func(this uintptr, qwLen uintptr) uintptr {
				return hrENotImpl
			}),
			GetCurrentPosition: syscall.NewCallback(func(this uintptr, pqwPos *uint64) uintptr {
				if pqwPos == nil {
					return hrEPointer
				}
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					return hrEFail
				}
				bs.mu.Lock()
				*pqwPos = uint64(bs.pos)
				bs.mu.Unlock()
				return hrOK
			}),
			SetCurrentPosition: syscall.NewCallback(func(this uintptr, qwPos uintptr) uintptr {
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					return hrEFail
				}
				bs.mu.Lock()
				target := int64(uint64(qwPos))
				if target < 0 {
					target = 0
				}
				bs.pos = target
				bs.mu.Unlock()
				return hrOK
			}),
			IsEndOfStream: syscall.NewCallback(func(this uintptr, pfEOS *int32) uintptr {
				if pfEOS == nil {
					return hrEPointer
				}
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					*pfEOS = 1
					return hrOK
				}
				bs.mu.Lock()
				if (bs.eof || bs.closed) && bs.pos >= bs.virtEnd {
					*pfEOS = 1
				} else {
					*pfEOS = 0
				}
				bs.mu.Unlock()
				return hrOK
			}),
			Read: syscall.NewCallback(func(this uintptr, pb *byte, cb uintptr, pcbRead *uint32) uintptr {
				if pcbRead != nil {
					*pcbRead = 0
				}
				if pb == nil && cb > 0 {
					return hrEPointer
				}
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					return hrEAbort
				}
				if cb == 0 {
					return hrOK
				}
				dst := unsafe.Slice(pb, int(cb))
				n, hr := bs.readBlocking(dst)
				if pcbRead != nil {
					*pcbRead = uint32(n)
				}
				return hr
			}),
			BeginRead: syscall.NewCallback(func(this uintptr, pb *byte, cb uintptr, pCallback uintptr, punkState uintptr) uintptr {
				if pCallback == 0 || (pb == nil && cb > 0) {
					return hrEInvalidArg
				}
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					return hrEAbort
				}
				var asyncRes uintptr
				hr, _, _ := procMFCreateAsyncResult.Call(0, pCallback, punkState, uintptr(unsafe.Pointer(&asyncRes)))
				if int32(hr) < 0 || asyncRes == 0 {
					return hr
				}

				bs.mu.Lock()
				if bs.closed {
					bs.mu.Unlock()
					comRelease(asyncRes)
					return hrEAbort
				}
				bs.refCount.Add(1)
				bs.wg.Add(1)
				bs.mu.Unlock()

				go func() {
					defer bs.releaseRef()
					defer bs.wg.Done()
					var n int
					status := hrOK
					if cb > 0 {
						tmp := make([]byte, int(cb))
						n, status = bs.readBlocking(tmp)
						// Guard native buffer write under bs.mu against concurrent abort/teardown.
						bs.mu.Lock()
						if bs.closed {
							n = 0
							status = hrEAbort
						} else if n > 0 {
							copy(unsafe.Slice(pb, n), tmp[:n])
						}
						bs.pendingReads[asyncRes] = mfAsyncReadOutcome{n: uint32(n), hr: status}
						bs.mu.Unlock()
					} else {
						bs.mu.Lock()
						if bs.closed {
							status = hrEAbort
						}
						bs.pendingReads[asyncRes] = mfAsyncReadOutcome{n: 0, hr: status}
						bs.mu.Unlock()
					}

					_ = vcall(asyncRes, 6, status)    // IMFAsyncResult::SetStatus
					_ = vcall(pCallback, 4, asyncRes) // IMFAsyncCallback::Invoke
					// Retain pendingReads[asyncRes] until EndRead consumes it (which MF may
					// invoke asynchronously after Invoke returns) or finalize cleans leftovers.
					comRelease(asyncRes)
				}()
				return hrOK
			}),
			EndRead: syscall.NewCallback(func(this uintptr, pResult uintptr, pcbRead *uint32) uintptr {
				if pcbRead == nil || pResult == 0 {
					return hrEPointer
				}
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					*pcbRead = 0
					return hrEAbort
				}
				bs.mu.Lock()
				out, ok := bs.pendingReads[pResult]
				if ok {
					delete(bs.pendingReads, pResult)
				} else if bs.closed {
					out = mfAsyncReadOutcome{n: 0, hr: hrEAbort}
				} else {
					out = mfAsyncReadOutcome{n: 0, hr: hrEInvalidArg}
				}
				bs.mu.Unlock()
				*pcbRead = out.n
				return out.hr
			}),
			Write: syscall.NewCallback(func(this uintptr, pb *byte, cb uintptr, pcbWritten *uint32) uintptr {
				return hrENotImpl
			}),
			BeginWrite: syscall.NewCallback(func(this uintptr, pb *byte, cb uintptr, pCallback uintptr, punkState uintptr) uintptr {
				return hrENotImpl
			}),
			EndWrite: syscall.NewCallback(func(this uintptr, pResult uintptr, pcbWritten *uint32) uintptr {
				return hrENotImpl
			}),
			Seek: syscall.NewCallback(func(this uintptr, origin uintptr, offset uintptr, flags uintptr, pqwPos *uint64) uintptr {
				bs := lookupMFLiveByteStream(this)
				if bs == nil {
					return hrEFail
				}
				bs.mu.Lock()
				var target int64
				if origin == 1 {
					target = bs.pos + int64(offset)
				} else {
					target = int64(offset)
				}
				if target < 0 {
					target = 0
				}
				bs.pos = target
				if pqwPos != nil {
					*pqwPos = uint64(bs.pos)
				}
				bs.mu.Unlock()
				return hrOK
			}),
			Flush: syscall.NewCallback(func(this uintptr) uintptr {
				return hrOK
			}),
			Close: syscall.NewCallback(func(this uintptr) uintptr {
				bs := lookupMFLiveByteStream(this)
				if bs != nil {
					bs.abort()
				}
				return hrOK
			}),
		}
	})
}

func newMFLiveByteStream(src *liveFMP4Source) (*mfLiveByteStream, error) {
	initMFLiveByteStreamVtbl()
	ensureProcessMTA()

	var mfErr error
	var attrs uintptr
	withThreadMTA(func() {
		r1, _, _ := procMFStartup.Call(mfVersionWin10, 0)
		if int32(r1) < 0 {
			mfErr = fmt.Errorf("mf: MFStartup failed: 0x%08x", uint32(r1))
			return
		}
		if hr, _, _ := procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&attrs)), 2); int32(hr) >= 0 && attrs != 0 {
			if mime, mErr := windows.UTF16PtrFromString("video/mp4"); mErr == nil {
				_ = vcall(attrs, 25, uintptr(unsafe.Pointer(&guidMFByteStreamContentType)), uintptr(unsafe.Pointer(mime))) // SetString
			}
			if name, nErr := windows.UTF16PtrFromString("live.mp4"); nErr == nil {
				_ = vcall(attrs, 25, uintptr(unsafe.Pointer(&guidMFByteStreamOriginName)), uintptr(unsafe.Pointer(name))) // SetString
			}
		}
	})
	if mfErr != nil {
		return nil, mfErr
	}

	f, err := openSharedReadFile(src.path)
	if err != nil {
		comRelease(attrs)
		_, _, _ = procMFShutdown.Call()
		return nil, err
	}

	bs := &mfLiveByteStream{
		com:            mfByteStreamCOMHeader{lpVtbl: &mfLiveVtbl},
		attrs:          attrs,
		mfStarted:      true,
		file:           f,
		realFileCursor: src.startFileOff,
		pendingReads:   make(map[uintptr]mfAsyncReadOutcome),
	}
	bs.cond = sync.NewCond(&bs.mu)
	bs.refCount.Store(1)

	if src.initEnd > 0 {
		bs.spans = append(bs.spans, mfVirtSpan{
			virtOff: 0,
			length:  src.initEnd,
			isPad:   false,
			fileOff: 0,
		})
		bs.virtEnd = src.initEnd
		bs.hasUnpadded = true
	}

	bs.comAddr = uintptr(unsafe.Pointer(&bs.com))
	mfLiveStreamMapMu.Lock()
	mfLiveStreamMap[bs.comAddr] = bs
	mfLiveStreamMapMu.Unlock()

	committedEnd, ended, closed := src.subscribe(func(nextCommitted int64, nextEnded bool, nextClosed bool) {
		if nextClosed {
			bs.abort()
			return
		}
		bs.onSourceUpdate(nextCommitted, nextEnded)
	})
	if closed {
		bs.abort()
		bs.releaseOwner()
		return nil, context.Canceled
	}
	bs.onSourceUpdate(committedEnd, ended)
	return bs, nil
}

func (bs *mfLiveByteStream) comPtr() uintptr {
	return bs.comAddr
}

func (bs *mfLiveByteStream) onSourceUpdate(committedEnd int64, ended bool) {
	bs.mu.Lock()
	if !bs.closed {
		if committedEnd > bs.realFileCursor {
			delta := committedEnd - bs.realFileCursor
			bs.spans = append(bs.spans, mfVirtSpan{
				virtOff: bs.virtEnd,
				length:  delta,
				isPad:   false,
				fileOff: bs.realFileCursor,
			})
			bs.virtEnd += delta
			bs.realFileCursor = committedEnd
			bs.hasUnpadded = true
		}
		if ended {
			bs.eof = true
		}
		bs.cond.Broadcast()
	}
	bs.mu.Unlock()
}

func (bs *mfLiveByteStream) readBlocking(dst []byte) (int, uintptr) {
	if len(dst) == 0 {
		return 0, hrOK
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()

	wantEnd := bs.pos + int64(len(dst))
	for {
		if bs.closed {
			return 0, hrEAbort
		}
		if wantEnd <= bs.virtEnd {
			break
		}
		if bs.hasUnpadded {
			padLen := wantEnd - bs.virtEnd
			if padLen < 16 {
				padLen = 16
			}
			if padLen > (1 << 28) {
				padLen = 1 << 28
			}
			bs.spans = append(bs.spans, mfVirtSpan{
				virtOff: bs.virtEnd,
				length:  padLen,
				isPad:   true,
			})
			bs.virtEnd += padLen
			bs.hasUnpadded = false
			break
		}
		if bs.eof {
			break
		}
		bs.cond.Wait()
	}

	if bs.pos >= bs.virtEnd {
		return 0, hrOK
	}
	avail := bs.virtEnd - bs.pos
	toRead := int64(len(dst))
	if toRead > avail {
		toRead = avail
	}
	n, err := bs.readSpanLocked(bs.pos, dst[:toRead])
	bs.pos += int64(n)
	if err != nil && n == 0 {
		if bs.closed {
			return 0, hrEAbort
		}
		return 0, hrEFail
	}
	return n, hrOK
}

func (bs *mfLiveByteStream) readSpanLocked(vOff int64, dst []byte) (int, error) {
	total := 0
	for total < len(dst) {
		cur := vOff + int64(total)
		idx := -1
		for i := len(bs.spans) - 1; i >= 0; i-- {
			sp := bs.spans[i]
			if cur >= sp.virtOff && cur < sp.virtOff+sp.length {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		sp := bs.spans[idx]
		inSpan := cur - sp.virtOff
		remSpan := sp.length - inSpan
		need := int64(len(dst) - total)
		if need > remSpan {
			need = remSpan
		}
		out := dst[total : total+int(need)]
		if sp.isPad {
			clear(out)
			if inSpan < 8 {
				var hdr [8]byte
				binary.BigEndian.PutUint32(hdr[0:4], uint32(sp.length))
				copy(hdr[4:8], "free")
				copy(out, hdr[inSpan:])
			}
			total += int(need)
			continue
		}
		if bs.file == nil {
			return total, io.EOF
		}
		n, err := bs.file.ReadAt(out, sp.fileOff+inSpan)
		total += n
		if int64(n) < need {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return total, err
		}
	}
	return total, nil
}

func (bs *mfLiveByteStream) abort() {
	bs.mu.Lock()
	if bs.closed {
		bs.mu.Unlock()
		return
	}
	bs.closed = true
	f := bs.file
	bs.file = nil
	bs.cond.Broadcast()
	bs.mu.Unlock()

	if f != nil {
		_ = f.Close()
	}
}

func (bs *mfLiveByteStream) releaseRef() int32 {
	rc := bs.refCount.Add(-1)
	if rc <= 0 {
		bs.abort()
		bs.finalize()
		return 0
	}
	return rc
}

func (bs *mfLiveByteStream) finalize() {
	bs.cleanOnce.Do(func() {
		mfLiveStreamMapMu.Lock()
		delete(mfLiveStreamMap, bs.comAddr)
		mfLiveStreamMapMu.Unlock()

		bs.mu.Lock()
		attrs := bs.attrs
		bs.attrs = 0
		clear(bs.pendingReads)
		mfStarted := bs.mfStarted
		bs.mfStarted = false
		bs.mu.Unlock()

		comRelease(attrs)
		if mfStarted {
			_, _, _ = procMFShutdown.Call()
		}
	})
}

func (bs *mfLiveByteStream) releaseOwner() {
	if bs.comAddr == 0 || !bs.ownerReleased.CompareAndSwap(false, true) {
		return
	}
	_ = bs.releaseRef()
}

func (bs *mfLiveByteStream) waitReads() {
	bs.abort()
	bs.wg.Wait()
}

func (bs *mfLiveByteStream) waitClosed() {
	bs.waitReads()
	bs.mu.Lock()
	clear(bs.pendingReads)
	bs.mu.Unlock()
}

func openLiveMFReader(byteStream uintptr, forVideo bool) (*MFDecoder, error) {
	return openMFReaderWithFactory(forVideo, false, func(attrs uintptr) (uintptr, error) {
		var reader uintptr
		hr, _, _ := procMFCreateSourceReaderFromByteStream.Call(
			byteStream,
			attrs,
			uintptr(unsafe.Pointer(&reader)),
		)
		if int32(hr) < 0 || reader == 0 {
			return 0, fmt.Errorf("mf: MFCreateSourceReaderFromByteStream failed: 0x%08x", uint32(hr))
		}
		return reader, nil
	})
}

type liveMFDecoder struct {
	src   *liveFMP4Source
	vbs   *mfLiveByteStream
	abs   *mfLiveByteStream
	video *MFDecoder
	audio *MFDecoder

	basePTSInit   bool
	basePTS       time.Duration
	hasFirstFrame bool
	videoEOS      atomic.Bool
	closed        atomic.Bool
	abortOnce     sync.Once
	closeOnce     sync.Once
}

func openLivePreviewPlatform(ctx context.Context, path string, opts LivePreviewOptions) (MediaDecoder, error) {
	src, err := openLiveFMP4Source(ctx, path, opts)
	if err != nil {
		return nil, err
	}
	if !src.hasVideo {
		_ = src.Close()
		return nil, ErrLivePreviewUnsupported
	}

	vbs, err := newMFLiveByteStream(src)
	if err != nil {
		_ = src.Close()
		return nil, err
	}

	var cancelMu sync.Mutex
	activeStreams := []*mfLiveByteStream{vbs}
	openDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cancelMu.Lock()
			streams := append([]*mfLiveByteStream(nil), activeStreams...)
			cancelMu.Unlock()
			for _, s := range streams {
				s.abort()
			}
			src.abort()
		case <-openDone:
		}
	}()

	videoDec, err := openLiveMFReader(vbs.comPtr(), true)
	if err != nil {
		close(openDone)
		vbs.waitClosed()
		vbs.releaseOwner()
		_ = src.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if IsMissingCodecError(err) {
			return nil, ErrMissingCodec
		}
		return nil, err
	}

	var abs *mfLiveByteStream
	var audioDec *MFDecoder
	if ctx.Err() == nil && src.hasAudio {
		if aStream, aErr := newMFLiveByteStream(src); aErr == nil {
			cancelMu.Lock()
			activeStreams = append(activeStreams, aStream)
			cancelMu.Unlock()
			if ctx.Err() != nil {
				aStream.waitClosed()
				aStream.releaseOwner()
			} else if aDec, rErr := openLiveMFReader(aStream.comPtr(), false); rErr == nil {
				abs = aStream
				audioDec = aDec
			} else {
				aStream.waitClosed()
				aStream.releaseOwner()
			}
		}
	}
	close(openDone)

	if err := ctx.Err(); err != nil {
		if abs != nil {
			abs.waitReads()
		}
		vbs.waitReads()
		if audioDec != nil {
			_ = audioDec.Close()
		}
		_ = videoDec.Close()
		if abs != nil {
			abs.waitClosed()
			abs.releaseOwner()
		}
		vbs.waitClosed()
		vbs.releaseOwner()
		_ = src.Close()
		return nil, err
	}

	return &liveMFDecoder{
		src:   src,
		vbs:   vbs,
		abs:   abs,
		video: videoDec,
		audio: audioDec,
	}, nil
}

func (d *liveMFDecoder) BackendName() string { return "windows-mf-live" }
func (d *liveMFDecoder) HasVideo() bool      { return d.video != nil && d.video.hasVideo }

func (d *liveMFDecoder) DecodePath() string {
	if d.video == nil {
		return "none"
	}
	return d.video.DecodePath()
}

func (d *liveMFDecoder) Width() int {
	if d.video == nil {
		return 0
	}
	return d.video.Width()
}

func (d *liveMFDecoder) Height() int {
	if d.video == nil {
		return 0
	}
	return d.video.Height()
}

func (d *liveMFDecoder) Duration() time.Duration { return 0 }

func (d *liveMFDecoder) FPS() float64 {
	if d.video == nil {
		return 30
	}
	return d.video.FPS()
}

func (d *liveMFDecoder) SampleRate() int {
	if d.audio == nil {
		return 0
	}
	return d.audio.SampleRate()
}

func (d *liveMFDecoder) Channels() int {
	if d.audio == nil {
		return 2
	}
	return d.audio.Channels()
}

func (d *liveMFDecoder) IsLive() bool { return true }

func (d *liveMFDecoder) LiveEnded() bool {
	if d.closed.Load() {
		return true
	}
	if d.src == nil {
		return true
	}
	return d.src.LiveEnded()
}

func (d *liveMFDecoder) AvailableDuration() time.Duration {
	if d.src == nil {
		return 0
	}
	return d.src.AvailableDuration()
}

func (d *liveMFDecoder) DecodeVideoFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	if d.closed.Load() || d.video == nil {
		return nil, 0, io.EOF
	}
	v := d.video
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.closed || v.reader == 0 {
		return nil, 0, io.EOF
	}
	if d.hasFirstFrame && v.lastFrame != nil && pos <= v.lastVideoPos {
		return v.lastFrame, v.lastVideoPos, nil
	}
	if d.videoEOS.Load() && v.lastFrame != nil {
		return v.lastFrame, v.lastVideoPos, io.EOF
	}

	for tries := 0; tries < 240; tries++ {
		if d.closed.Load() {
			return v.lastFrame, v.lastVideoPos, io.EOF
		}
		var actualIdx uint32
		var flags uint32
		var tsHNS int64
		var sample uintptr

		hr := vcall(
			v.reader,
			9, // ReadSample
			uintptr(mfSourceReaderFirstVideoStream),
			0,
			uintptr(unsafe.Pointer(&actualIdx)),
			uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&tsHNS)),
			uintptr(unsafe.Pointer(&sample)),
		)
		if int32(hr) < 0 {
			if d.closed.Load() || d.src.LiveEnded() {
				d.videoEOS.Store(true)
				return v.lastFrame, v.lastVideoPos, io.EOF
			}
			return v.fallbackFrameLocked(dst, pos), pos, fmt.Errorf("mf-live: ReadSample(video) failed: 0x%08x", uint32(hr))
		}
		if sample != 0 {
			rawPTS := time.Duration(tsHNS * 100)
			if !d.basePTSInit {
				d.basePTS = rawPTS
				if d.src.originPTSKnown && d.src.originPTS < d.basePTS {
					d.basePTS = d.src.originPTS
				}
				d.basePTSInit = true
			} else if rawPTS < d.basePTS {
				d.basePTS = rawPTS
			}
			relPTS := rawPTS - d.basePTS
			if relPTS < 0 {
				relPTS = 0
			}

			avail := d.src.AvailableDuration()
			if d.hasFirstFrame && pos-relPTS > 45*time.Millisecond && relPTS+250*time.Millisecond < avail {
				comRelease(sample)
				v.lastVideoPos = relPTS
				continue
			}

			rgba := v.extractVideoSample(sample, dst)
			comRelease(sample)
			v.lastVideoPos = relPTS
			v.lastFrame = rgba
			d.hasFirstFrame = true
			return rgba, relPTS, nil
		}
		if (flags & mfSourceReaderFlagEndOfStream) != 0 {
			d.videoEOS.Store(true)
			if v.lastFrame != nil {
				return v.lastFrame, v.lastVideoPos, io.EOF
			}
			return v.fallbackFrameLocked(dst, pos), pos, io.EOF
		}
		if (flags & mfSourceReaderFlagError) != 0 {
			if d.closed.Load() || d.src.LiveEnded() {
				d.videoEOS.Store(true)
				return v.lastFrame, v.lastVideoPos, io.EOF
			}
			return v.fallbackFrameLocked(dst, pos), pos, fmt.Errorf("mf-live: video stream error (flags=0x%x)", flags)
		}
	}
	return v.fallbackFrameLocked(dst, pos), pos, fmt.Errorf("mf-live: video ReadSample yielded no sample")
}

func (d *liveMFDecoder) ReadAudioSamples(buf []float32) (int, error) {
	if d.closed.Load() || d.audio == nil {
		return 0, io.EOF
	}
	return d.audio.ReadAudioSamples(buf)
}

func (d *liveMFDecoder) Seek(pos time.Duration) error {
	return nil
}

func (d *liveMFDecoder) SeekAudio(pos time.Duration) error {
	return nil
}

func (d *liveMFDecoder) abort() {
	d.abortOnce.Do(func() {
		d.closed.Store(true)
		if d.src != nil {
			d.src.abort()
		}
		if d.vbs != nil {
			d.vbs.abort()
		}
		if d.abs != nil {
			d.abs.abort()
		}
	})
}

func (d *liveMFDecoder) Close() error {
	d.abort()
	d.closeOnce.Do(func() {
		// Wait for all in-flight BeginRead goroutines to complete with E_ABORT
		// while IMFSourceReader and native MF buffers are still alive, then
		// release readers before dropping the owner COM refs on the byte streams.
		if d.abs != nil {
			d.abs.waitReads()
		}
		if d.vbs != nil {
			d.vbs.waitReads()
		}
		if d.audio != nil {
			_ = d.audio.Close()
		}
		if d.video != nil {
			_ = d.video.Close()
		}
		if d.abs != nil {
			d.abs.waitClosed()
			d.abs.releaseOwner()
		}
		if d.vbs != nil {
			d.vbs.waitClosed()
			d.vbs.releaseOwner()
		}
		if d.src != nil {
			_ = d.src.Close()
		}
	})
	return nil
}
