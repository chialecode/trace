//go:build windows

package player

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Hardware-accelerated decoding (F-043). A D3D11 device handed to the Source
// Reader through an IMFDXGIDeviceManager lets Media Foundation use the GPU
// decoder and the GPU video processor for color conversion and scaling.
// Every step is optional: any failure falls back to the software path.

var (
	modD3D11 = windows.NewLazySystemDLL("d3d11.dll")

	procD3D11CreateDevice         = modD3D11.NewProc("D3D11CreateDevice")
	procMFCreateDXGIDeviceManager = modMFPlat.NewProc("MFCreateDXGIDeviceManager")
)

const (
	d3dDriverTypeHardware        = 1
	d3d11CreateDeviceBGRASupport = 0x20
	d3d11CreateDeviceVideo       = 0x800
	d3d11SDKVersion              = 7

	mfSourceReaderAllStreams = 0xFFFFFFFE

	// maxDecodeLongSide bounds decoded frames: the window never shows more,
	// and every extra pixel is read back, uploaded and mip-mapped per frame.
	maxDecodeLongSide = 1920

	// MFNominalRange_0_255: the video processor otherwise keeps studio
	// range (black 16, white 235) in RGB, which looks washed out.
	mfNominalRange0To255 = 1
)

var (
	// MF_SOURCE_READER_D3D_MANAGER {ec822da2-e1e9-4b29-a0d8-563c719f5269}
	guidMFSourceReaderD3DManager = windows.GUID{
		Data1: 0xec822da2, Data2: 0xe1e9, Data3: 0x4b29,
		Data4: [8]byte{0xa0, 0xd8, 0x56, 0x3c, 0x71, 0x9f, 0x52, 0x69},
	}
	// MF_SOURCE_READER_ENABLE_ADVANCED_VIDEO_PROCESSING {0f81da2c-b537-4672-a8b2-a681b17307a3}
	guidMFSourceReaderEnableAdvancedVideoProcessing = windows.GUID{
		Data1: 0x0f81da2c, Data2: 0xb537, Data3: 0x4672,
		Data4: [8]byte{0xa8, 0xb2, 0xa6, 0x81, 0xb1, 0x73, 0x07, 0xa3},
	}
	// MF_READWRITE_ENABLE_HARDWARE_TRANSFORMS {a634a91c-822b-41b9-a494-4de4643612b0}
	guidMFReadWriteEnableHardwareTransforms = windows.GUID{
		Data1: 0xa634a91c, Data2: 0x822b, Data3: 0x41b9,
		Data4: [8]byte{0xa4, 0x94, 0x4d, 0xe4, 0x64, 0x36, 0x12, 0xb0},
	}
	// MFVideoFormat_ABGR32 (D3DFMT_A8B8G8R8): bytes in memory are R, G, B, A.
	guidMFVideoFormatABGR32 = windows.GUID{
		Data1: 0x00000020, Data2: 0x0000, Data3: 0x0010,
		Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71},
	}
	// MF_MT_VIDEO_NOMINAL_RANGE {c21b8ee5-b956-4071-8daf-325edf5cab11}
	guidMFMTVideoNominalRange = windows.GUID{
		Data1: 0xc21b8ee5, Data2: 0xb956, Data3: 0x4071,
		Data4: [8]byte{0x8d, 0xaf, 0x32, 0x5e, 0xdf, 0x5c, 0xab, 0x11},
	}
	// IID_ID3D10Multithread {9B7E4E00-342C-4106-A19F-4F2704F689F0}
	iidID3D10Multithread = windows.GUID{
		Data1: 0x9b7e4e00, Data2: 0x342c, Data3: 0x4106,
		Data4: [8]byte{0xa1, 0x9f, 0x4f, 0x27, 0x04, 0xf6, 0x89, 0xf0},
	}
)

// newDXGIDeviceManager creates a video-capable D3D11 device wrapped in an
// IMFDXGIDeviceManager. It returns zeros when the machine has no usable GPU.
func newDXGIDeviceManager() (device, manager uintptr) {
	if procD3D11CreateDevice.Find() != nil || procMFCreateDXGIDeviceManager.Find() != nil {
		return 0, 0
	}
	hr, _, _ := procD3D11CreateDevice.Call(
		0, d3dDriverTypeHardware, 0,
		d3d11CreateDeviceVideo|d3d11CreateDeviceBGRASupport,
		0, 0, d3d11SDKVersion,
		uintptr(unsafe.Pointer(&device)), 0, 0,
	)
	if int32(hr) < 0 || device == 0 {
		return 0, 0
	}
	// The decoder and the reader's video processor share the device across
	// threads, which D3D11 allows only with multithread protection.
	var mt uintptr
	if hr := vcall(device, 0, uintptr(unsafe.Pointer(&iidID3D10Multithread)), uintptr(unsafe.Pointer(&mt))); int32(hr) >= 0 && mt != 0 {
		_ = vcall(mt, 5, 1) // SetMultithreadProtected(TRUE)
		comRelease(mt)
	}
	var token uint32
	hr, _, _ = procMFCreateDXGIDeviceManager.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&manager)))
	if int32(hr) < 0 || manager == 0 {
		comRelease(device)
		return 0, 0
	}
	if hr := vcall(manager, 7, device, uintptr(token)); int32(hr) < 0 { // ResetDevice
		comRelease(manager)
		comRelease(device)
		return 0, 0
	}
	return device, manager
}

func comSetUnknown(obj uintptr, key *windows.GUID, val uintptr) error {
	if hr := vcall(obj, 27, uintptr(unsafe.Pointer(key)), val); int32(hr) < 0 {
		return windows.Errno(hr)
	}
	return nil
}

// decodeSize fits w×h within maxDecodeLongSide, keeping the aspect ratio and
// even dimensions as video processors require.
func decodeSize(w, h int) (int, int) {
	long := max(w, h)
	if w <= 0 || h <= 0 || long <= maxDecodeLongSide {
		return w, h
	}
	tw := (w*maxDecodeLongSide/long + 1) &^ 1
	th := (h*maxDecodeLongSide/long + 1) &^ 1
	return max(2, tw), max(2, th)
}
