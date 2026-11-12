package sign

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testDesktopChromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"

type decodedABogus struct {
	HeaderMagic   [2]byte
	HeaderNoise   [2]byte
	SDKVersion    [4]byte
	ProbeNoise    byte
	TripwireNoise byte
	NowMs         int64
	InkMs         int64
	Fortnights    byte
	PageID        uint32
	AID           uint32
	QuerySlots    [3]byte
	BodySlots     [3]byte
	UASlots       [3]byte
	BrowserInfo   []byte
	Tail          []byte
	Checksum      byte
	ZeroOmitted   bool
}

func decodeCustomBase64(alphabet, encoded string) ([]byte, error) {
	if encoded == "" || len(encoded)%4 != 0 {
		return nil, fmt.Errorf("invalid encoded length %d", len(encoded))
	}
	var rev [256]int
	for i := range rev {
		rev[i] = -1
	}
	for i := 0; i < len(alphabet); i++ {
		rev[alphabet[i]] = i
	}

	out := make([]byte, 0, (len(encoded)/4)*3)
	for off := 0; off < len(encoded); off += 4 {
		chunk := encoded[off : off+4]
		pad := 0
		var vals [4]int
		for j := 0; j < 4; j++ {
			c := chunk[j]
			if c == '=' {
				if off+4 != len(encoded) || j < 2 || (j == 2 && chunk[3] != '=') {
					return nil, fmt.Errorf("invalid padding at offset %d", off+j)
				}
				pad++
				continue
			}
			if pad > 0 {
				return nil, fmt.Errorf("data after padding at offset %d", off+j)
			}
			idx := rev[c]
			if idx < 0 {
				return nil, fmt.Errorf("invalid alphabet char %q at %d", c, off+j)
			}
			vals[j] = idx
		}
		acc := (uint32(vals[0]) << 18) | (uint32(vals[1]) << 12) | (uint32(vals[2]) << 6) | uint32(vals[3])
		out = append(out, byte(acc>>16))
		if pad < 2 {
			out = append(out, byte(acc>>8))
		}
		if pad < 1 {
			out = append(out, byte(acc))
		}
	}
	return out, nil
}

func deinterleavePair(c [4]byte) (p0, p1, nLow, nHigh byte) {
	p0 = (c[0] & 0x55) | (c[1] & 0xAA)
	p1 = (c[2] & 0x55) | (c[3] & 0xAA)
	nLow = (c[0] & 0xAA) | (c[1] & 0x55)
	nHigh = (c[2] & 0xAA) | (c[3] & 0x55)
	return
}

func collapseBodyGroups(expanded []byte) []byte {
	out := make([]byte, 0, (len(expanded)/4)*3+3)
	for off := 0; off < len(expanded); off += 4 {
		rem := expanded[off:]
		if len(rem) >= 4 {
			out = append(out,
				(rem[0]&0x6E)|(rem[3]&0x91),
				(rem[1]&0xBD)|(rem[3]&0x42),
				(rem[2]&0xD3)|(rem[3]&0x2C),
			)
		} else {
			out = append(out, rem...)
		}
	}
	return out
}

// decodeABogus implements the reverse structural verifier from docs/research/douyin.md#sign-spec §A.6.
func decodeABogus(sig string) (*decodedABogus, error) {
	wire, err := decodeCustomBase64(alphabetWire, sig)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	if len(wire) < 64 {
		return nil, fmt.Errorf("wire too short: %d", len(wire))
	}

	var hdrArr [4]byte
	copy(hdrArr[:], wire[0:4])
	hm0, hm1, hn0, hn1 := deinterleavePair(hdrArr)
	if hm0 != 3 || hm1 != 82 {
		return nil, fmt.Errorf("unexpected header magic (%d, %d)", hm0, hm1)
	}

	plain := transformModifiedRC4([]byte{payloadStreamKey}, wire[4:])
	if len(plain) < 8 {
		return nil, fmt.Errorf("inner payload too short: %d", len(plain))
	}

	var verLo, verHi [4]byte
	copy(verLo[:], plain[0:4])
	copy(verHi[:], plain[4:8])
	v0, v1, _, _ := deinterleavePair(verLo)
	v2, v3, nProbe, nTripwire := deinterleavePair(verHi)
	if v0 != 1 || v1 != 0 || v2 != 1 || v3 != 0 {
		return nil, fmt.Errorf("unexpected sdk version (%d,%d,%d,%d)", v0, v1, v2, v3)
	}
	if nProbe >= 110 && nProbe <= 240 && nProbe%2 == 0 {
		return nil, fmt.Errorf("probe noise %d landed in forbidden even range 110..240", nProbe)
	}
	if nProbe > 241 {
		return nil, fmt.Errorf("probe noise %d exceeded max 241", nProbe)
	}
	if (nTripwire & 0xB2) != 0xB2 {
		return nil, fmt.Errorf("tripwire noise 0x%02x missing mandatory 0xB2 bits", nTripwire)
	}

	unpacked := collapseBodyGroups(plain[8:])
	if len(unpacked) < 50 {
		return nil, fmt.Errorf("unpacked body shorter than 50 scalars: %d", len(unpacked))
	}
	sc := unpacked[:50]

	if sc[43] != 41 {
		return nil, fmt.Errorf("marker slot = %d, want 41", sc[43])
	}
	if sc[14] != 3 {
		return nil, fmt.Errorf("elapsed slot = %d, want 3", sc[14])
	}
	if sc[28] != 6 {
		return nil, fmt.Errorf("bucket slot = %d, want 6", sc[28])
	}
	if sc[11] != 3 {
		return nil, fmt.Errorf("tripLock slot = %d, want 3", sc[11])
	}
	envFlags := uint16(sc[8]) | (uint16(sc[42]) << 8)
	if envFlags != 1 {
		return nil, fmt.Errorf("envFlags = %d, want 1", envFlags)
	}
	nrFlags := uint16(sc[10]) | (uint16(sc[35]) << 8)
	if nrFlags != 0x21 {
		return nil, fmt.Errorf("nrFlags = 0x%x, want 0x21", nrFlags)
	}
	detectFlags := uint32(sc[1]) | (uint32(sc[7]) << 8) | (uint32(sc[41]) << 16) | (uint32(sc[17]) << 24)
	if detectFlags != 14 {
		return nil, fmt.Errorf("detectFlags = %d, want 14", detectFlags)
	}
	if sc[25] != 0 || sc[30] != 0 || sc[34] != 0 || sc[39] != 0 {
		return nil, fmt.Errorf("nrTag non-zero: (%d,%d,%d,%d)", sc[25], sc[30], sc[34], sc[39])
	}

	nowMs := int64(uint64(sc[5]) |
		(uint64(sc[18]) << 8) |
		(uint64(sc[22]) << 16) |
		(uint64(sc[45]) << 24) |
		(uint64(sc[36]) << 32) |
		(uint64(sc[0]) << 40))

	inkMs := int64(uint64(sc[44]) |
		(uint64(sc[3]) << 8) |
		(uint64(sc[26]) << 16) |
		(uint64(sc[27]) << 24) |
		(uint64(sc[16]) << 32) |
		(uint64(sc[40]) << 40))

	if inkMs != nowMs-1 {
		return nil, fmt.Errorf("inkMs %d != nowMs-1 (%d)", inkMs, nowMs-1)
	}
	wantFortnights := byte(((nowMs - fortnightEpochMs) / fortnightSpanMs) & 0xFF)
	if sc[20] != wantFortnights {
		return nil, fmt.Errorf("fortnights = %d, want %d", sc[20], wantFortnights)
	}

	pageID := uint32(sc[37]) | (uint32(sc[13]) << 8) | (uint32(sc[23]) << 16) | (uint32(sc[6]) << 24)
	aid := uint32(sc[19]) | (uint32(sc[29]) << 8) | (uint32(sc[4]) << 16) | (uint32(sc[31]) << 24)
	infoLen := int(uint16(sc[46]) | (uint16(sc[47]) << 8))
	tailLen := int(uint16(sc[48]) | (uint16(sc[49]) << 8))

	end := 50 + infoLen + tailLen
	expectedFullLen := end + 1

	var expectedChecksum byte
	for _, b := range plain[0:8] {
		expectedChecksum ^= b
	}
	for _, b := range sc {
		expectedChecksum ^= b
	}

	zeroOmitted := false
	switch len(unpacked) {
	case expectedFullLen:
		if expectedFullLen%3 == 2 && unpacked[end] == 0 {
			return nil, fmt.Errorf("trailing zero checksum should have been omitted for mod3==2")
		}
		if unpacked[end] != expectedChecksum {
			return nil, fmt.Errorf("checksum mismatch: got 0x%02x, want 0x%02x", unpacked[end], expectedChecksum)
		}
	case end:
		if expectedFullLen%3 != 2 || expectedChecksum != 0 {
			return nil, fmt.Errorf("truncated unpacked len=%d (want %d) with mod3=%d checksum=0x%02x",
				len(unpacked), expectedFullLen, expectedFullLen%3, expectedChecksum)
		}
		zeroOmitted = true
	default:
		return nil, fmt.Errorf("unexpected unpacked length %d, want %d", len(unpacked), expectedFullLen)
	}

	return &decodedABogus{
		HeaderMagic:   [2]byte{hm0, hm1},
		HeaderNoise:   [2]byte{hn0, hn1},
		SDKVersion:    [4]byte{v0, v1, v2, v3},
		ProbeNoise:    nProbe,
		TripwireNoise: nTripwire,
		NowMs:         nowMs,
		InkMs:         inkMs,
		Fortnights:    sc[20],
		PageID:        pageID,
		AID:           aid,
		QuerySlots:    [3]byte{sc[15], sc[9], sc[12]},
		BodySlots:     [3]byte{sc[33], sc[38], sc[21]},
		UASlots:       [3]byte{sc[2], sc[32], sc[24]},
		BrowserInfo:   append([]byte(nil), unpacked[50:50+infoLen]...),
		Tail:          append([]byte(nil), unpacked[50+infoLen:end]...),
		Checksum:      expectedChecksum,
		ZeroOmitted:   zeroOmitted,
	}, nil
}

func TestSM3StandardVectors(t *testing.T) {
	cases := []struct {
		name string
		msg  []byte
		want string
	}{
		{
			name: "GBT32905_abc",
			msg:  []byte("abc"),
			want: "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0",
		},
		{
			name: "GBT32905_abcd_x16",
			msg:  []byte(strings.Repeat("abcd", 16)),
			want: "debe9ff92275b8a138604889c18e5a4d6fdb70e5387e5765293dcba39c0c5732",
		},
		{
			name: "Empty",
			msg:  nil,
			want: "1ab21d8355cfa17f8e61194831e81a8f22bec8c728fefb747ed035eb5082aa2b",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sum := SumSM3(tc.msg)
			got := hex.EncodeToString(sum[:])
			if got != tc.want {
				t.Fatalf("SumSM3(%q) = %s, want %s", tc.name, got, tc.want)
			}
		})
	}

	// Verify padding boundary continuity around 55/56/63/64/128 bytes.
	for _, n := range []int{1, 55, 56, 57, 63, 64, 65, 119, 120, 128} {
		buf := bytes.Repeat([]byte{'x'}, n)
		d1 := SumSM3(buf)
		d2 := SumSM3(append([]byte(nil), buf...))
		if d1 != d2 {
			t.Fatalf("non-deterministic SM3 at length %d", n)
		}
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode %q: %v", s, err)
	}
	return b
}

func TestSignSpecDeterministicVectors(t *testing.T) {
	ctx := context.Background()

	t.Run("Vector1_Unprotected_Mod3Eq0", func(t *testing.T) {
		entropy := mustHex(t, "030a11181f262d343b424950575e656c737a81888f969da4abb2b9c0c7ced5dce3eaf1f8ff060d")
		entReader := bytes.NewReader(entropy)

		res, err := Sign(ctx, SignInput{
			Endpoint:  "/aweme/v1/web/user/profile/other/",
			UserAgent: testDesktopChromeUA,
			Params: []Pair{
				{Key: "device_platform", Value: "webapp"},
				{Key: "aid", Value: "6383"},
				{Key: "channel", Value: "channel_pc_web"},
				{Key: "sec_user_id", Value: "MS4wLjABAAAA_SYNTHETIC_USER_01"},
				{Key: "msToken", Value: "SYNTHETIC_TOKEN_01"},
			},
			Now:     func() time.Time { return time.UnixMilli(1763000000097) },
			Entropy: entReader,
		})
		if err != nil {
			t.Fatalf("Sign failed: %v", err)
		}
		if entReader.Len() != 0 {
			t.Fatalf("unread entropy bytes: %d", entReader.Len())
		}

		const wantSig = "dJUnkqSiQoWbKd/tmcnD7tnUTLy/NB8y8TidWraTHxP4OhzcC8PoPENWaNErszEkLRBhiH17YxFMbExcTtUwZeHpwmpvSkTfdU5VVgmLZqwsGzkQErjzew6zLw0rURsql55tiIRI/UJ96VnAwHdE/BA9tKoC55WBM3OWk/ubP9B6ZMyAD3nHPBSpYhiO0HVR"
		const wantPct = "dJUnkqSiQoWbKd%2FtmcnD7tnUTLy%2FNB8y8TidWraTHxP4OhzcC8PoPENWaNErszEkLRBhiH17YxFMbExcTtUwZeHpwmpvSkTfdU5VVgmLZqwsGzkQErjzew6zLw0rURsql55tiIRI%2FUJ96VnAwHdE%2FBA9tKoC55WBM3OWk%2FubP9B6ZMyAD3nHPBSpYhiO0HVR"

		if res.Signature != wantSig {
			t.Fatalf("Signature mismatch:\ngot:  %s\nwant: %s", res.Signature, wantSig)
		}
		wantQuery := "device_platform=webapp&aid=6383&channel=channel_pc_web&sec_user_id=MS4wLjABAAAA_SYNTHETIC_USER_01&msToken=SYNTHETIC_TOKEN_01&a_bogus=" + wantPct
		if res.SignedQuery != wantQuery {
			t.Fatalf("SignedQuery mismatch:\ngot:  %s\nwant: %s", res.SignedQuery, wantQuery)
		}
		if len(res.Headers) != 0 || res.Stage2Signature != "" {
			t.Fatalf("unexpected stage2 output: headers=%v sig2=%q", res.Headers, res.Stage2Signature)
		}

		dec, err := decodeABogus(res.Signature)
		if err != nil {
			t.Fatalf("decodeABogus failed: %v", err)
		}
		if dec.Checksum != 0xFD || dec.ZeroOmitted || dec.NowMs != 1763000000097 {
			t.Fatalf("unexpected decoded metadata: %+v", dec)
		}
	})

	t.Run("Vector2_Protected_Stage2_VerifyFp_Mod3Eq1", func(t *testing.T) {
		entropy := mustHex(t, "05101b26313c47525d68737e89949faab5c0cbd6e1ecf7020d18232e39444f5a65707b86919c")
		entReader := bytes.NewReader(entropy)

		res, err := Sign(ctx, SignInput{
			Endpoint:  "/aweme/v1/web/aweme/detail/",
			UserAgent: testDesktopChromeUA,
			UIFID:     "SYNTHETIC_UIFID_02",
			VerifyFp:  "verify_synthetic_fp_02",
			Params: []Pair{
				{Key: "device_platform", Value: "webapp"},
				{Key: "aid", Value: "6383"},
				{Key: "channel", Value: "channel_pc_web"},
				{Key: "aweme_id", Value: "7300000000000000001"},
				{Key: "msToken", Value: "SYNTHETIC_TOKEN_02"},
			},
			Now:     func() time.Time { return time.UnixMilli(1763000000000) },
			Entropy: entReader,
		})
		if err != nil {
			t.Fatalf("Sign failed: %v", err)
		}
		if entReader.Len() != 0 {
			t.Fatalf("unread entropy bytes: %d", entReader.Len())
		}

		const wantSig = "mv0fhqywmZWnKdKGmCcY7talj9j/rTWy6TTOboNPtxOeOqUc5mNOPYxAGNxN4MDkgWpkieIHTxzlbddcTGXzZC9kompvuOXW24VcVgvo0qqsGMvQDrbsez6FKwMNUbTqeAVvil46hUr96nVAkHQY/pAySKLC5c8BP3x6k2ucT9Bg1zgAD1c-PdSkKh52"
		const wantSecSig = "8a3f27d0959b92823749f4cf46d54644"

		if res.Signature != wantSig {
			t.Fatalf("Signature mismatch:\ngot:  %s\nwant: %s", res.Signature, wantSig)
		}
		if res.Stage2Signature != wantSecSig {
			t.Fatalf("Stage2Signature = %s, want %s", res.Stage2Signature, wantSecSig)
		}
		if res.Headers[HeaderUIFID] != "SYNTHETIC_UIFID_02" ||
			res.Headers[QueryParamSecSDKSignature] != wantSecSig ||
			res.Headers[HeaderSecSDKExpire] != "1763000000" {
			t.Fatalf("unexpected Headers: %#v", res.Headers)
		}
		if strings.Contains(res.SignedQuery, HeaderSecSDKExpire) {
			t.Fatalf("x-secsdk-web-expire leaked into SignedQuery: %s", res.SignedQuery)
		}

		dec, err := decodeABogus(res.Signature)
		if err != nil {
			t.Fatalf("decodeABogus failed: %v", err)
		}
		if dec.Checksum != 0xCD || dec.ZeroOmitted {
			t.Fatalf("unexpected decoded checksum state: %+v", dec)
		}
	})

	t.Run("Vector3_TrailingZeroChecksumOmission_Mod3Eq2_PreexistingUIFID", func(t *testing.T) {
		entropy := mustHex(t, "da1623303d4a5764717e8b98a5b2bfccd9e6f3000d1a2734414e5b6875828f9ca9b6c3d0ddea")
		entReader := bytes.NewReader(entropy)

		res, err := Sign(ctx, SignInput{
			Endpoint:  "/aweme/v1/web/aweme/post/",
			UserAgent: testDesktopChromeUA,
			UIFID:     "SYNTHETIC_UIFID_03",
			Params: []Pair{
				{Key: "device_platform", Value: "webapp"},
				{Key: "aid", Value: "6383"},
				{Key: "sec_user_id", Value: "MS4wLjABAAAA_SYNTHETIC_USER_03"},
				{Key: "max_cursor", Value: "0"},
				{Key: "count", Value: "18"},
				{Key: "uifid", Value: "SYNTHETIC_UIFID_03"},
				{Key: "msToken", Value: "SYNTHETIC_TOKEN_03"},
			},
			Now:     func() time.Time { return time.UnixMilli(1763000000007) },
			Entropy: entReader,
		})
		if err != nil {
			t.Fatalf("Sign failed: %v", err)
		}
		if entReader.Len() != 0 {
			t.Fatalf("unread entropy bytes: %d", entReader.Len())
		}

		const wantSig = "QvUnDFUjmq85OdKt8CcY7SrUG2L/rPuy5TiKRFnPHPz1chMa5mN2PYxWjNxN40DkgRBwie1HxxzlbEVczGXhZHHpqmkvSwiyxU5AV0fo0qN4GzkQDHbheuXzFwMr0csqe55XilR62UJ9gVdAirQY/Bl9tKoCQcSBB1OfkMucN9B6Z06AD3c1PptpNhiq"
		const wantSecSig = "668d0b177abc76c8f89e56a95b45e575"

		if res.Signature != wantSig {
			t.Fatalf("Signature mismatch:\ngot:  %s\nwant: %s", res.Signature, wantSig)
		}
		if res.Stage2Signature != wantSecSig {
			t.Fatalf("Stage2Signature = %s, want %s", res.Stage2Signature, wantSecSig)
		}
		if gotCount := strings.Count(res.SignedQuery, "uifid="); gotCount != 1 {
			t.Fatalf("expected uifid= once in SignedQuery, got %d: %s", gotCount, res.SignedQuery)
		}

		dec, err := decodeABogus(res.Signature)
		if err != nil {
			t.Fatalf("decodeABogus failed on zero-omitted boundary: %v", err)
		}
		if dec.Checksum != 0x00 || !dec.ZeroOmitted {
			t.Fatalf("expectedChecksum=0x00 and ZeroOmitted=true, got %+v", dec)
		}
		if string(dec.Tail) != "10," {
			t.Fatalf("decoded tail = %q, want %q", string(dec.Tail), "10,")
		}
	})
}

func TestDecodeABogusStructuralSelfCheck(t *testing.T) {
	ctx := context.Background()
	browsers := []struct {
		ua       string
		wantBase byte
	}{
		{ua: testDesktopChromeUA, wantBase: 0},
		{ua: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/136.0.0.0 Safari/537.36 Edg/136.0.0.0", wantBase: 125},
		{ua: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:135.0) Gecko/20100101 Firefox/135.0", wantBase: 40},
		{ua: "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_4) AppleWebKit/605.1.15 Version/17.4 Safari/605.1.15", wantBase: 81},
		{ua: "Mozilla/5.0 (Linux; HarmonyOS; HuaweiBrowser/15.0)", wantBase: 170},
		{ua: "CustomDesktopClient/1.0", wantBase: 210},
	}

	for idx, br := range browsers {
		nowMs := int64(1763000000000 + idx*53)
		q := "aid=6383&seq=" + strconv.Itoa(idx)
		body := ""
		ct := ""
		if idx == 2 {
			body = `{"synthetic":true}`
			ct = "application/json"
		} else if idx == 3 {
			body = "--boundary\r\nignored"
			ct = "multipart/form-data; boundary=boundary"
		}

		res, err := Sign(ctx, SignInput{
			Endpoint:    "/aweme/v1/web/user/profile/self/",
			RawQuery:    q,
			Body:        body,
			ContentType: ct,
			UserAgent:   br.ua,
			PageID:      9999,
			AID:         6383,
			Now:         func() time.Time { return time.UnixMilli(nowMs) },
		})
		if err != nil {
			t.Fatalf("Sign[%d] failed: %v", idx, err)
		}

		dec, err := decodeABogus(res.Signature)
		if err != nil {
			t.Fatalf("decodeABogus[%d] failed: %v", idx, err)
		}
		if dec.AID != 6383 || dec.PageID != 9999 || dec.NowMs != nowMs {
			t.Fatalf("slot mismatch[%d]: %+v", idx, dec)
		}
		fam := dec.HeaderNoise[1]
		if fam < br.wantBase || fam >= br.wantBase+40 {
			t.Fatalf("browser family noise %d outside [%d, %d)", fam, br.wantBase, br.wantBase+40)
		}

		// Verify all 3 digest chains and canary bytes match independent calculation.
		dq := doubleSM3String(q + abogusSalt)
		effBody := body
		if strings.Contains(strings.ToLower(ct), "multipart/form-data") {
			effBody = ""
		}
		db := doubleSM3String(effBody + abogusSalt)
		uaEnc := encodeCustomBase64(alphabetUA, transformModifiedRC4([]byte{0, 1, 14}, encodeUTF16Units(br.ua)))
		dua := SumSM3([]byte(uaEnc))

		if dec.QuerySlots != [3]byte{dq[9], dq[18], pickCanaryByte(dq, 11, 3, 12)} {
			t.Fatalf("query digest slots mismatch: got %v", dec.QuerySlots)
		}
		if dec.BodySlots != [3]byte{db[10], db[19], pickCanaryByte(db, 8, 4, 9)} {
			t.Fatalf("body digest slots mismatch: got %v", dec.BodySlots)
		}
		if dec.UASlots != [3]byte{dua[11], dua[21], pickCanaryByte(dua, 12, 5, 13)} {
			t.Fatalf("ua digest slots mismatch: got %v", dec.UASlots)
		}
	}

	// Tamper detection: flipping a wire byte inside the body must fail checksum.
	good, err := Sign(ctx, SignInput{
		Endpoint:  "/aweme/v1/web/user/profile/self/",
		RawQuery:  "aid=6383",
		UserAgent: testDesktopChromeUA,
		Now:       func() time.Time { return time.UnixMilli(1763000000100) },
	})
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	wire, err := decodeCustomBase64(alphabetWire, good.Signature)
	if err != nil {
		t.Fatalf("decode wire: %v", err)
	}
	wire[20] ^= 0x04
	tampered := encodeCustomBase64(alphabetWire, wire)
	if _, err := decodeABogus(tampered); err == nil {
		t.Fatalf("expected tampered signature to fail structural verification")
	}
}

func TestQueryOrderPreservedAndNoDoubleEncoding(t *testing.T) {
	ctx := context.Background()
	params := []Pair{
		{Key: "z_last", Value: "MS4wLjABAAAA_a-b_c__1"},
		{Key: "a_first", Value: "space and/slash+plus"},
		{Key: "m_mid", Value: "中文"},
	}

	res, err := Sign(ctx, SignInput{
		Endpoint:  "/aweme/v1/web/aweme/detail/",
		Params:    params,
		UIFID:     "SYNTHETIC_UIFID_ORDER",
		UserAgent: testDesktopChromeUA,
		Now:       func() time.Time { return time.UnixMilli(1763000000500) },
	})
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	const wantPrefix = "z_last=MS4wLjABAAAA_a-b_c__1&a_first=space%20and%2Fslash%2Bplus&m_mid=%E4%B8%AD%E6%96%87&a_bogus="
	if !strings.HasPrefix(res.SignedQuery, wantPrefix) {
		t.Fatalf("query order or single-encoding broken:\ngot:  %s\nwant prefix: %s", res.SignedQuery, wantPrefix)
	}
	if strings.Contains(res.SignedQuery, "%25") {
		t.Fatalf("detected double percent-encoding in SignedQuery: %s", res.SignedQuery)
	}
}

func TestSecSDKRulesAndValidation(t *testing.T) {
	ctx := context.Background()
	nowFn := func() time.Time { return time.UnixMilli(1763000000000) }

	// 1. Protected path without uifid -> stage 1 only.
	r1, err := Sign(ctx, SignInput{
		Endpoint:  "https://www.douyin.com/aweme/v1/web/aweme/detail/?ignored=1",
		Params:    []Pair{{Key: "aid", Value: "6383"}},
		UserAgent: testDesktopChromeUA,
		Now:       nowFn,
	})
	if err != nil {
		t.Fatalf("Sign r1: %v", err)
	}
	if r1.Stage2Signature != "" || len(r1.Headers) != 0 || strings.Contains(r1.SignedQuery, QueryParamSecSDKSignature) {
		t.Fatalf("expected stage 1 only when uifid is empty, got %+v", r1)
	}

	// 2. Unprotected path with uifid -> stage 1 only.
	for _, ep := range []string{
		"/aweme/v1/web/user/profile/other/",
		"/aweme/v1/web/user/profile/self/",
		"/webcast/room/web/enter/",
	} {
		r, err := Sign(ctx, SignInput{
			Endpoint:  ep,
			Params:    []Pair{{Key: "aid", Value: "6383"}},
			UIFID:     "SYNTHETIC_UIFID",
			VerifyFp:  "verify_synthetic",
			UserAgent: testDesktopChromeUA,
			Now:       nowFn,
		})
		if err != nil {
			t.Fatalf("Sign(%s): %v", ep, err)
		}
		if r.Stage2Signature != "" || len(r.Headers) != 0 || strings.Contains(r.SignedQuery, QueryParamSecSDKSignature) {
			t.Fatalf("endpoint %s should not attach stage 2 signature: %+v", ep, r)
		}
	}

	// 3. All 14 protected paths with uifid -> stage 2 attached, x-secsdk-web-expire header-only.
	protectedList := []string{
		"/aweme/v1/web/aweme/detail/",
		"/aweme/v1/web/aweme/post",
		"https://www.douyin.com/aweme/v1/web/aweme/favorite/",
		"/aweme/v1/web/aweme/listcollection/",
		"/aweme/v1/web/mix/aweme/",
		"/aweme/v1/web/tab/feed/",
		"/aweme/v1/web/mix/list/",
		"/aweme/v1/web/music/aweme/",
		"/aweme/v1/web/music/list/",
		"/aweme/v1/web/mix/detail/",
		"/aweme/v1/web/mix/listcollection/",
		"/aweme/v1/web/music/detail/",
		"/aweme/v1/web/collects/list/",
		"/aweme/v1/web/collects/video/list/",
	}
	for _, ep := range protectedList {
		r, err := Sign(ctx, SignInput{
			Endpoint:  ep,
			Params:    []Pair{{Key: "aid", Value: "6383"}},
			UIFID:     "SYNTHETIC_UIFID",
			UserAgent: testDesktopChromeUA,
			Now:       nowFn,
		})
		if err != nil {
			t.Fatalf("Sign(%s): %v", ep, err)
		}
		if len(r.Stage2Signature) != 32 || r.Headers[HeaderSecSDKExpire] != "1763000000" {
			t.Fatalf("endpoint %s missing stage 2 signature: %+v", ep, r)
		}
		if strings.Contains(r.SignedQuery, HeaderSecSDKExpire) {
			t.Fatalf("endpoint %s leaked x-secsdk-web-expire into query: %s", ep, r.SignedQuery)
		}
	}

	// 4. ExtractUIFID priority order & token helpers.
	gotUIFID := ExtractUIFID(map[string]string{
		"UIFID":      "upper_uifid",
		"uifid_temp": "lower_temp",
	})
	if gotUIFID != "lower_temp" {
		t.Fatalf("ExtractUIFID = %q, want %q", gotUIFID, "lower_temp")
	}

	tok, err := GenerateMSToken(bytes.NewReader(bytes.Repeat([]byte{1, 2, 3}, 50)))
	if err != nil || len(tok) != 128 || !strings.HasSuffix(tok, "==") {
		t.Fatalf("GenerateMSToken = %q (len=%d), err=%v", tok, len(tok), err)
	}
	wid, err := GenerateWebID(bytes.NewReader(bytes.Repeat([]byte{4, 5, 6}, 10)))
	if err != nil || len(wid) != 19 || wid[0] != '7' {
		t.Fatalf("GenerateWebID = %q (len=%d), err=%v", wid, len(wid), err)
	}

	// 5. Validation errors: empty UA, pre-epoch clock, canceled context.
	if _, err := Sign(ctx, SignInput{Now: nowFn}); !errors.Is(err, ErrEmptyUserAgent) {
		t.Fatalf("expected ErrEmptyUserAgent, got %v", err)
	}
	if _, err := Sign(ctx, SignInput{
		UserAgent: testDesktopChromeUA,
		Now:       func() time.Time { return time.UnixMilli(fortnightEpochMs - 1) },
	}); !errors.Is(err, ErrClockBeforeEpoch) {
		t.Fatalf("expected ErrClockBeforeEpoch, got %v", err)
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Sign(canceledCtx, SignInput{UserAgent: testDesktopChromeUA, Now: nowFn}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
