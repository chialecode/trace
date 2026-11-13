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

var defaultEntropy = []byte(BilibiliEntropy)

func deriveFallbackKey(entropy []byte) [32]byte {
	if len(entropy) == 0 {
		entropy = defaultEntropy
	}
	buf := make([]byte, 0, len(entropy)+len(":fallback"))
	buf = append(buf, entropy...)
	buf = append(buf, ":fallback"...)
	return sha256.Sum256(buf)
}

func encryptBytes(plain []byte) ([]byte, error) {
	return encryptBytesWithEntropy(plain, defaultEntropy)
}

func decryptBytes(cipherData []byte) ([]byte, error) {
	return decryptBytesWithEntropy(cipherData, defaultEntropy)
}

func encryptBytesWithEntropy(plain []byte, entropy []byte) ([]byte, error) {
	key := deriveFallbackKey(entropy)
	block, err := aes.NewCipher(key[:])
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

func decryptBytesWithEntropy(cipherData []byte, entropy []byte) ([]byte, error) {
	if !bytes.HasPrefix(cipherData, []byte("DPAPI1")) {
		return nil, fmt.Errorf("credential: invalid cipher header")
	}
	raw := cipherData[6:]
	key := deriveFallbackKey(entropy)
	block, err := aes.NewCipher(key[:])
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
