//go:build !windows

package credential

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

var nonWindowsKey = sha256.Sum256([]byte("trace:bilibili:credential:v1:fallback"))

func encryptBytes(plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(nonWindowsKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := gcm.Seal(nonce, nonce, plain, nil)
	return append([]byte("DPAPI1"), out...), nil
}

func decryptBytes(cipherData []byte) ([]byte, error) {
	if !bytes.HasPrefix(cipherData, []byte("DPAPI1")) {
		return nil, fmt.Errorf("credential: invalid cipher header")
	}
	raw := cipherData[6:]
	block, err := aes.NewCipher(nonWindowsKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("credential: cipher too short")
	}
	nonce := raw[:gcm.NonceSize()]
	ct := raw[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}
