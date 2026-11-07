//go:build windows

package credential

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var dpapiEntropy = []byte("trace:bilibili:credential:v1")

func makeBlob(b []byte) windows.DataBlob {
	if len(b) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{
		Size: uint32(len(b)),
		Data: &b[0],
	}
}

func encryptBytes(plain []byte) ([]byte, error) {
	inBlob := makeBlob(plain)
	entropyBlob := makeBlob(dpapiEntropy)
	var outBlob windows.DataBlob

	err := windows.CryptProtectData(
		&inBlob,
		nil,
		&entropyBlob,
		0,
		nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN,
		&outBlob,
	)
	if err != nil {
		return nil, fmt.Errorf("credential: CryptProtectData failed: %w", err)
	}
	defer func() {
		if outBlob.Data != nil {
			_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
		}
	}()

	out := make([]byte, outBlob.Size)
	copy(out, unsafe.Slice(outBlob.Data, outBlob.Size))
	return out, nil
}

func decryptBytes(cipher []byte) ([]byte, error) {
	if len(cipher) == 0 {
		return nil, fmt.Errorf("credential: empty cipher blob")
	}
	inBlob := makeBlob(cipher)
	entropyBlob := makeBlob(dpapiEntropy)
	var outBlob windows.DataBlob

	err := windows.CryptUnprotectData(
		&inBlob,
		nil,
		&entropyBlob,
		0,
		nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN,
		&outBlob,
	)
	if err != nil {
		return nil, fmt.Errorf("credential: CryptUnprotectData failed: %w", err)
	}
	defer func() {
		if outBlob.Data != nil {
			// Zero out decrypted memory before freeing.
			slice := unsafe.Slice(outBlob.Data, outBlob.Size)
			for i := range slice {
				slice[i] = 0
			}
			_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
		}
	}()

	out := make([]byte, outBlob.Size)
	copy(out, unsafe.Slice(outBlob.Data, outBlob.Size))
	return out, nil
}
