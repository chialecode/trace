package x

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func buildSyntheticSVGPaths() []string {
	// 4 distinct synthetic SVG paths with 16 C-rows each (11 values per row in 0..255).
	paths := make([]string, 4)
	for frame := 0; frame < 4; frame++ {
		var b strings.Builder
		b.WriteString("M 10 10")
		for row := 0; row < 16; row++ {
			r0 := (10 + frame*15 + row*7) % 256
			g0 := (20 + frame*17 + row*9) % 256
			b0 := (30 + frame*19 + row*11) % 256
			r1 := (180 + frame*11 + row*5) % 256
			g1 := (190 + frame*13 + row*3) % 256
			b1 := (200 + frame*7 + row*2) % 256
			rot := (64 + frame*23 + row*13) % 256
			c0 := (40 + frame*11 + row*8) % 256
			c1 := (51 + frame*9 + row*12) % 256
			c2 := (170 + frame*7 + row*5) % 256
			c3 := (210 + frame*5 + row*3) % 256
			fmt.Fprintf(&b, " C %d %d %d %d %d %d %d %d %d %d %d",
				r0, g0, b0, r1, g1, b1, rot, c0, c1, c2, c3)
		}
		paths[frame] = b.String()
	}
	return paths
}

func syntheticKeyBytes48() []byte {
	key := make([]byte, 48)
	for i := range key {
		key[i] = byte(0x10 + i*3)
	}
	// Deterministic indices for golden tests:
	// key[5] % 4 == 2  -> SVGPaths[2]
	// key[7] % 16 == 3 -> row 3
	// key[12]%16=8, key[19]%16=10, key[25]%16=13 -> product 1040 -> targetTime 1040/4096
	key[5] = 0x22
	key[7] = 0x13
	key[12] = 0x18
	key[19] = 0x2a
	key[25] = 0x3d
	return key
}

func TestTxIDStage1AnalyticalIdentityAtZeroProgress(t *testing.T) {
	key := syntheticKeyBytes48()
	key[12] = 0x10 // 0x10 % 16 == 0 => frameProduct == 0 => targetTime == 0 => progress == 0

	// Frame 2 (key[5]=0x22 => 2), Row 3 (key[7]=0x13 => 3):
	// At progress = 0: color = fromColor (10, 20, 30) = "a", "14", "1e";
	// degrees = 0 => cos(0)=1, sin(0)=0 => matrix [1, 0, 0, 1] => "1001", plus trailing "00".
	paths := buildSyntheticSVGPaths()
	var customFrame strings.Builder
	customFrame.WriteString("M 10 10")
	for row := 0; row < 16; row++ {
		if row == 3 {
			customFrame.WriteString(" C 10 20 30 210 220 230 153 85 170 170 213")
		} else {
			customFrame.WriteString(" C 1 2 3 4 5 6 7 8 9 10 11")
		}
	}
	paths[2] = customFrame.String()

	mat := TxIDMaterial{
		VerificationKey: base64.StdEncoding.EncodeToString(key),
		SVGPaths:        paths,
		RowIndex:        7,
		KeyByteIndices:  []int{12, 19, 25},
	}

	gen, err := NewTxIDGenerator(mat)
	if err != nil {
		t.Fatalf("NewTxIDGenerator unexpected error: %v", err)
	}
	const wantAnimKey = "a141e100100"
	if got := gen.AnimationKey(); got != wantAnimKey {
		t.Fatalf("AnimationKey() = %q, want %q", got, wantAnimKey)
	}
}

func TestTxIDGoldenVectorDeterministicAndWireFormat(t *testing.T) {
	key := syntheticKeyBytes48()
	paths := buildSyntheticSVGPaths()
	mat := TxIDMaterial{
		VerificationKey: base64.StdEncoding.EncodeToString(key),
		SVGPaths:        paths,
		RowIndex:        7,
		KeyByteIndices:  []int{12, 19, 25},
	}

	// Fixed clock: 2026-11-22T06:00:00Z => unix 1795327200 => seconds = 112402800 (0x06B32170).
	fixedNow := time.Unix(1795327200, 0).UTC()
	const fixedMask byte = 0x5A

	gen, err := NewTxIDGenerator(
		mat,
		WithTxIDClock(func() time.Time { return fixedNow }),
		WithTxIDRandByte(func() (byte, error) { return fixedMask, nil }),
	)
	if err != nil {
		t.Fatalf("NewTxIDGenerator unexpected error: %v", err)
	}

	animKey := gen.AnimationKey()
	if animKey == "" {
		t.Fatalf("AnimationKey() returned empty string")
	}
	for i := 0; i < len(animKey); i++ {
		c := animKey[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("AnimationKey() contains non-hex char %q in %q", c, animKey)
		}
	}
	if !strings.HasSuffix(animKey, "00") {
		t.Fatalf("AnimationKey() = %q, want trailing translation zeros \"00\"", animKey)
	}

	const method = "GET"
	const path = "/i/api/graphql/SynthQidUserTweets03/UserTweets"
	got, err := gen.Generate(method, path)
	if err != nil {
		t.Fatalf("Generate unexpected error: %v", err)
	}
	if strings.Contains(got, "=") {
		t.Fatalf("Generate output %q must be unpadded Base64", got)
	}

	// Verify exact wire-level protocol layout per docs/research/x.md §10.3.
	raw, err := base64.RawStdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("base64.RawStdEncoding.DecodeString failed: %v", err)
	}
	wantLen := 1 + len(key) + 4 + 16 + 1
	if len(raw) != wantLen {
		t.Fatalf("decoded length = %d, want %d", len(raw), wantLen)
	}
	if raw[0] != fixedMask {
		t.Fatalf("mask byte = 0x%02X, want 0x%02X", raw[0], fixedMask)
	}
	plain := make([]byte, len(raw)-1)
	for i := range plain {
		plain[i] = raw[1+i] ^ raw[0]
	}

	gotKey := plain[:len(key)]
	gotTime := plain[len(key) : len(key)+4]
	gotDigest := plain[len(key)+4 : len(key)+20]
	gotTail := plain[len(key)+20]

	if !bytes.Equal(gotKey, key) {
		t.Fatalf("unmasked keyBytes mismatch")
	}
	wantSeconds := uint32(fixedNow.Unix() - TxIDEpochUnix)
	if gotSeconds := binary.LittleEndian.Uint32(gotTime); gotSeconds != wantSeconds {
		t.Fatalf("unmasked seconds = %d, want %d", gotSeconds, wantSeconds)
	}
	wantDigest := sha256.Sum256([]byte(fmt.Sprintf("%s!%s!%d%s%s", method, path, wantSeconds, TxIDKeyword, animKey)))
	if !bytes.Equal(gotDigest, wantDigest[:16]) {
		t.Fatalf("unmasked digest = %x, want %x", gotDigest, wantDigest[:16])
	}
	if gotTail != TxIDAdditionalByte {
		t.Fatalf("trailing byte = %d, want %d", gotTail, TxIDAdditionalByte)
	}

	// Pin exact golden AnimationKey and full Base64 output (independently verified above).
	const wantGoldenAnimKey = "43576a0fd70a3d70a3d702b851eb851eb8602b851eb851eb860fd70a3d70a3d700"
	if animKey != wantGoldenAnimKey {
		t.Fatalf("golden AnimationKey = %q, want %q", animKey, wantGoldenAnimKey)
	}
	wantPlain := make([]byte, 0, len(key)+4+16+1)
	wantPlain = append(wantPlain, key...)
	var wantTimeBytes [4]byte
	binary.LittleEndian.PutUint32(wantTimeBytes[:], wantSeconds)
	wantPlain = append(wantPlain, wantTimeBytes[:]...)
	wantPlain = append(wantPlain, wantDigest[:16]...)
	wantPlain = append(wantPlain, TxIDAdditionalByte)
	wantMasked := make([]byte, 1+len(wantPlain))
	wantMasked[0] = fixedMask
	for i, b := range wantPlain {
		wantMasked[1+i] = b ^ fixedMask
	}
	wantGoldenTxID := base64.RawStdEncoding.EncodeToString(wantMasked)
	if got != wantGoldenTxID {
		t.Fatalf("golden x-client-transaction-id = %q, want %q", got, wantGoldenTxID)
	}
}

func TestTxIDMethodPathAndQueryNormalization(t *testing.T) {
	mat := TxIDMaterial{
		VerificationKey: base64.StdEncoding.EncodeToString(syntheticKeyBytes48()),
		SVGPaths:        buildSyntheticSVGPaths(),
		RowIndex:        7,
		KeyByteIndices:  []int{12, 19, 25},
	}
	fixedNow := time.Unix(1795327200, 0).UTC()
	gen, err := NewTxIDGenerator(
		mat,
		WithTxIDClock(func() time.Time { return fixedNow }),
		WithTxIDRandByte(func() (byte, error) { return 0x23, nil }),
	)
	if err != nil {
		t.Fatalf("NewTxIDGenerator failed: %v", err)
	}

	getUsers, err := gen.Generate("GET", "/i/api/graphql/SynthQidUserByScreenName01/UserByScreenName")
	if err != nil {
		t.Fatalf("Generate GET failed: %v", err)
	}
	// Lowercase method and appended query string normalize to identical signed path.
	getUsersLowerWithQuery, err := gen.Generate("get", "/i/api/graphql/SynthQidUserByScreenName01/UserByScreenName?variables=%7B%7D")
	if err != nil {
		t.Fatalf("Generate get+query failed: %v", err)
	}
	if getUsers != getUsersLowerWithQuery {
		t.Fatalf("method/query normalization mismatch: %q vs %q", getUsers, getUsersLowerWithQuery)
	}

	postUsers, err := gen.Generate("POST", "/i/api/graphql/SynthQidUserByScreenName01/UserByScreenName")
	if err != nil {
		t.Fatalf("Generate POST failed: %v", err)
	}
	if getUsers == postUsers {
		t.Fatalf("GET and POST produced identical txid %q", getUsers)
	}

	getDetail, err := gen.Generate("GET", "/i/api/graphql/SynthQidTweetDetail04/TweetDetail")
	if err != nil {
		t.Fatalf("Generate TweetDetail failed: %v", err)
	}
	if getUsers == getDetail {
		t.Fatalf("distinct paths produced identical txid %q", getUsers)
	}
}

func TestTxIDDefaultClockAndRandomness(t *testing.T) {
	mat := TxIDMaterial{
		VerificationKey: base64.RawStdEncoding.EncodeToString(syntheticKeyBytes48()), // unpadded key accepted
		SVGPaths:        buildSyntheticSVGPaths(),
		RowIndex:        7,
		KeyByteIndices:  []int{12, 19, 25},
	}
	gen, err := NewTxIDGenerator(mat)
	if err != nil {
		t.Fatalf("NewTxIDGenerator failed: %v", err)
	}
	tok, err := gen.Generate("GET", "/i/api/graphql/SynthQidFollowing05/Following")
	if err != nil {
		t.Fatalf("Generate with default clock/rand failed: %v", err)
	}
	if tok == "" {
		t.Fatalf("Generate returned empty token")
	}
	if _, err := base64.RawStdEncoding.DecodeString(tok); err != nil {
		t.Fatalf("token is not valid RawStdBase64: %v", err)
	}
	// Defensive copy check on KeyBytes.
	kb := gen.KeyBytes()
	orig0 := kb[0]
	kb[0] ^= 0xFF
	if gen.KeyBytes()[0] != orig0 {
		t.Fatalf("KeyBytes mutated internal slice")
	}
}

func TestParseOndemandIndices(t *testing.T) {
	js := `function f(e){return parseInt(e[7],16)+parseInt(e[12], 16)*parseInt($k[19],16)+parseInt(_x[25],16)}`
	rowIdx, keyIdxs, err := ParseOndemandIndices(js)
	if err != nil {
		t.Fatalf("ParseOndemandIndices unexpected error: %v", err)
	}
	if rowIdx != 7 {
		t.Fatalf("rowIdx = %d, want 7", rowIdx)
	}
	want := []int{12, 19, 25}
	if len(keyIdxs) != len(want) {
		t.Fatalf("keyIdxs = %v, want %v", keyIdxs, want)
	}
	for i := range want {
		if keyIdxs[i] != want[i] {
			t.Fatalf("keyIdxs[%d] = %d, want %d", i, keyIdxs[i], want[i])
		}
	}

	// Only 1 index found -> schema_changed.
	if _, _, err := ParseOndemandIndices(`parseInt(e[7], 16)`); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("ParseOndemandIndices(single) error = %v, want schema_changed", err)
	}
	if _, _, err := ParseOndemandIndices(`no indices here`); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("ParseOndemandIndices(empty) error = %v, want schema_changed", err)
	}
}

func TestTxIDMalformedMaterialAndGenerateErrors(t *testing.T) {
	validKey := base64.StdEncoding.EncodeToString(syntheticKeyBytes48())
	validPaths := buildSyntheticSVGPaths()

	badMaterials := []struct {
		name string
		mat  TxIDMaterial
	}{
		{
			name: "empty verification key",
			mat:  TxIDMaterial{VerificationKey: "", SVGPaths: validPaths, RowIndex: 7, KeyByteIndices: []int{12}},
		},
		{
			name: "invalid base64 key",
			mat:  TxIDMaterial{VerificationKey: "!!!not-base64!!!", SVGPaths: validPaths, RowIndex: 7, KeyByteIndices: []int{12}},
		},
		{
			name: "key too short for frame selection",
			mat:  TxIDMaterial{VerificationKey: base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4, 5}), SVGPaths: validPaths, RowIndex: 1, KeyByteIndices: []int{2}},
		},
		{
			name: "wrong svg path count",
			mat:  TxIDMaterial{VerificationKey: validKey, SVGPaths: validPaths[:3], RowIndex: 7, KeyByteIndices: []int{12}},
		},
		{
			name: "row index out of bounds",
			mat:  TxIDMaterial{VerificationKey: validKey, SVGPaths: validPaths, RowIndex: 99, KeyByteIndices: []int{12}},
		},
		{
			name: "empty key byte indices",
			mat:  TxIDMaterial{VerificationKey: validKey, SVGPaths: validPaths, RowIndex: 7, KeyByteIndices: nil},
		},
		{
			name: "key byte index out of bounds",
			mat:  TxIDMaterial{VerificationKey: validKey, SVGPaths: validPaths, RowIndex: 7, KeyByteIndices: []int{12, 80}},
		},
		{
			name: "svg missing C command",
			mat:  TxIDMaterial{VerificationKey: validKey, SVGPaths: []string{"M 0 0", "M 0 0", "M 0 0", "M 0 0"}, RowIndex: 7, KeyByteIndices: []int{12}},
		},
		{
			name: "svg too few rows",
			mat:  TxIDMaterial{VerificationKey: validKey, SVGPaths: []string{"M 0 0 C 1 2 3 4 5 6 7 8 9 10 11", "M 0 0 C 1 2 3 4 5 6 7 8 9 10 11", "M 0 0 C 1 2 3 4 5 6 7 8 9 10 11", "M 0 0 C 1 2 3 4 5 6 7 8 9 10 11"}, RowIndex: 7, KeyByteIndices: []int{12}},
		},
		{
			name: "svg row too few parameters",
			mat: func() TxIDMaterial {
				shortRow := "M 0 0" + strings.Repeat(" C 1 2 3 4 5", 16)
				return TxIDMaterial{VerificationKey: validKey, SVGPaths: []string{shortRow, shortRow, shortRow, shortRow}, RowIndex: 7, KeyByteIndices: []int{12}}
			}(),
		},
	}

	for _, tc := range badMaterials {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewTxIDGenerator(tc.mat); !site.IsKind(err, site.ErrKindSchemaChanged) {
				t.Fatalf("NewTxIDGenerator(%s) error = %v, want schema_changed", tc.name, err)
			}
		})
	}

	// Nil generator.
	var nilGen *TxIDGenerator
	if _, err := nilGen.Generate("GET", "/i/api/graphql/a/b"); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("nil Generate error = %v, want schema_changed", err)
	}

	// Empty method / path.
	gen, err := NewTxIDGenerator(TxIDMaterial{
		VerificationKey: validKey,
		SVGPaths:        validPaths,
		RowIndex:        7,
		KeyByteIndices:  []int{12, 19, 25},
	})
	if err != nil {
		t.Fatalf("valid generator failed: %v", err)
	}
	if _, err := gen.Generate("", "/i/api/graphql/a/b"); !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("empty method error = %v, want bad_request", err)
	}
	if _, err := gen.Generate("GET", "?only=query"); !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("empty path error = %v, want bad_request", err)
	}

	// Clock before epoch.
	oldGen, err := NewTxIDGenerator(
		TxIDMaterial{VerificationKey: validKey, SVGPaths: validPaths, RowIndex: 7, KeyByteIndices: []int{12}},
		WithTxIDClock(func() time.Time { return time.Unix(TxIDEpochUnix-1, 0) }),
	)
	if err != nil {
		t.Fatalf("oldGen construction failed: %v", err)
	}
	if _, err := oldGen.Generate("GET", "/i/api/graphql/a/b"); !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("pre-epoch clock error = %v, want bad_request", err)
	}

	// Rand error propagation.
	randErr := errors.New("synthetic rng failure")
	badRandGen, err := NewTxIDGenerator(
		TxIDMaterial{VerificationKey: validKey, SVGPaths: validPaths, RowIndex: 7, KeyByteIndices: []int{12}},
		WithTxIDRandByte(func() (byte, error) { return 0, randErr }),
	)
	if err != nil {
		t.Fatalf("badRandGen construction failed: %v", err)
	}
	if _, err := badRandGen.Generate("GET", "/i/api/graphql/a/b"); !errors.Is(err, randErr) {
		t.Fatalf("rand error = %v, want %v", err, randErr)
	}

	// Bezier and floatToHex math checks.
	if got := evalCubicBezier(1.0/3.0, 1.0/3.0, 2.0/3.0, 2.0/3.0, 0.37); math.Abs(got-0.37) > 1e-6 {
		t.Fatalf("linear bezier(0.37) = %v, want 0.37", got)
	}
	if got := floatToHex(0.5); got != "0.8" {
		t.Fatalf("floatToHex(0.5) = %q, want \"0.8\"", got)
	}
	if got := floatToHex(0.0); got != "0" {
		t.Fatalf("floatToHex(0.0) = %q, want \"0\"", got)
	}
	if got := floatToHex(1.0); got != "1" {
		t.Fatalf("floatToHex(1.0) = %q, want \"1\"", got)
	}
}
