// Package sign implements clean-room request signing for Douyin Web endpoints
// strictly from docs/research/douyin.md#sign-spec using only the Go standard library.
package sign

import (
	"encoding/binary"
	"math/bits"
)

var sm3InitialState = [8]uint32{
	0x7380166F,
	0x4914B2B9,
	0x172442D7,
	0xDA8A0600,
	0xA96F30BC,
	0x163138AA,
	0xE38DEE4D,
	0xB0FB0E4E,
}

const (
	sm3ConstLow  uint32 = 0x79CC4519
	sm3ConstHigh uint32 = 0x7A879D8A
)

// SumSM3 computes the 256-bit (32-byte) SM3 cryptographic hash digest of data
// as defined in GB/T 32905-2016.
func SumSM3(data []byte) [32]byte {
	state := sm3InitialState

	fullBlocks := len(data) &^ 63
	for off := 0; off < fullBlocks; off += 64 {
		sm3CompressBlock(&state, data[off:off+64])
	}

	// Final block(s) with 0x80 + zero padding + 64-bit big-endian bit length.
	rem := data[fullBlocks:]
	var tail [128]byte
	copy(tail[:], rem)
	tail[len(rem)] = 0x80

	totalTail := 64
	if len(rem) >= 56 {
		totalTail = 128
	}
	binary.BigEndian.PutUint64(tail[totalTail-8:totalTail], uint64(len(data))*8)

	sm3CompressBlock(&state, tail[:64])
	if totalTail == 128 {
		sm3CompressBlock(&state, tail[64:128])
	}

	var digest [32]byte
	for i, word := range state {
		binary.BigEndian.PutUint32(digest[4*i:4*i+4], word)
	}
	return digest
}

func sm3CompressBlock(state *[8]uint32, block []byte) {
	var w [68]uint32
	for j := 0; j < 16; j++ {
		w[j] = binary.BigEndian.Uint32(block[4*j : 4*j+4])
	}
	for j := 16; j < 68; j++ {
		mixed := w[j-16] ^ w[j-9] ^ bits.RotateLeft32(w[j-3], 15)
		p1 := mixed ^ bits.RotateLeft32(mixed, 15) ^ bits.RotateLeft32(mixed, 23)
		w[j] = p1 ^ bits.RotateLeft32(w[j-13], 7) ^ w[j-6]
	}

	a, b, c, d := state[0], state[1], state[2], state[3]
	e, f, g, h := state[4], state[5], state[6], state[7]

	for j := 0; j < 64; j++ {
		t := sm3ConstLow
		if j >= 16 {
			t = sm3ConstHigh
		}
		rotA12 := bits.RotateLeft32(a, 12)
		ss1 := bits.RotateLeft32(rotA12+e+bits.RotateLeft32(t, j), 7)
		ss2 := ss1 ^ rotA12

		var ff, gg uint32
		if j < 16 {
			ff = a ^ b ^ c
			gg = e ^ f ^ g
		} else {
			ff = (a & b) | (a & c) | (b & c)
			gg = (e & f) | (^e & g)
		}

		wPrime := w[j] ^ w[j+4]
		tt1 := ff + d + ss2 + wPrime
		tt2 := gg + h + ss1 + w[j]

		d = c
		c = bits.RotateLeft32(b, 9)
		b = a
		a = tt1
		h = g
		g = bits.RotateLeft32(f, 19)
		f = e
		e = tt2 ^ bits.RotateLeft32(tt2, 9) ^ bits.RotateLeft32(tt2, 17)
	}

	state[0] ^= a
	state[1] ^= b
	state[2] ^= c
	state[3] ^= d
	state[4] ^= e
	state[5] ^= f
	state[6] ^= g
	state[7] ^= h
}
