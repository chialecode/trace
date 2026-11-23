package x

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"
)

func TestSnowflakeGoldenVectorsBidirectional(t *testing.T) {
	// All 4 synthetic golden vectors from docs/research/x.md §4.2.
	vectors := []struct {
		name        string
		id          string
		hexValue    uint64
		deltaMS     uint64
		tsMS        uint64
		utcRFC3339  string
		dc          uint8
		worker      uint8
		seq         uint16
		isSnowflake bool
	}{
		{
			name:        "SV-1 epoch zero lower bound",
			id:          "0",
			hexValue:    0x0000000000000000,
			deltaMS:     0,
			tsMS:        1288834974657,
			utcRFC3339:  "2010-11-04T01:42:54.657Z",
			dc:          0,
			worker:      0,
			seq:         0,
			isSnowflake: false,
		},
		{
			name:        "SV-2 domain migration date regular bits",
			id:          "1791257321273778509",
			hexValue:    0x18DBD30B0FD7614D,
			deltaMS:     427069025343,
			tsMS:        1715904000000,
			utcRFC3339:  "2024-05-17T00:00:00.000Z",
			dc:          11,
			worker:      22,
			seq:         333,
			isSnowflake: true,
		},
		{
			name:        "SV-3 research date max sequence",
			id:          "2124382367241412607",
			hexValue:    0x1D7B5281EE871FFF,
			deltaMS:     506492225466,
			tsMS:        1795327200123,
			utcRFC3339:  "2026-11-22T06:00:00.123Z",
			dc:          3,
			worker:      17,
			seq:         4095,
			isSnowflake: true,
		},
		{
			name:        "SV-4 63-bit signed upper bound",
			id:          "9223372036854775807",
			hexValue:    0x7FFFFFFFFFFFFFFF,
			deltaMS:     2199023255551,
			tsMS:        3487858230208,
			utcRFC3339:  "2080-07-10T17:30:30.208Z",
			dc:          31,
			worker:      31,
			seq:         4095,
			isSnowflake: true,
		},
	}

	for _, tc := range vectors {
		t.Run(tc.name, func(t *testing.T) {
			// Forward: decimal string -> decoded fields
			decoded, err := DecodeSnowflakeRaw(tc.id)
			if err != nil {
				t.Fatalf("DecodeSnowflakeRaw(%q) unexpected error: %v", tc.id, err)
			}
			assertSnowflakeMatches := func(label string, got Snowflake) {
				t.Helper()
				if got.ID != tc.id {
					t.Errorf("%s: ID = %q, want %q", label, got.ID, tc.id)
				}
				if got.Value != tc.hexValue {
					t.Errorf("%s: Value = 0x%016X, want 0x%016X", label, got.Value, tc.hexValue)
				}
				if got.IsSnowflake != tc.isSnowflake {
					t.Errorf("%s: IsSnowflake = %v, want %v", label, got.IsSnowflake, tc.isSnowflake)
				}
				if got.DeltaMS != tc.deltaMS {
					t.Errorf("%s: DeltaMS = %d, want %d", label, got.DeltaMS, tc.deltaMS)
				}
				if got.TimestampMS != tc.tsMS {
					t.Errorf("%s: TimestampMS = %d, want %d", label, got.TimestampMS, tc.tsMS)
				}
				if gotUTC := got.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"); gotUTC != tc.utcRFC3339 {
					t.Errorf("%s: Timestamp = %s, want %s", label, gotUTC, tc.utcRFC3339)
				}
				if got.DatacenterID != tc.dc {
					t.Errorf("%s: DatacenterID = %d, want %d", label, got.DatacenterID, tc.dc)
				}
				if got.WorkerID != tc.worker {
					t.Errorf("%s: WorkerID = %d, want %d", label, got.WorkerID, tc.worker)
				}
				if got.Sequence != tc.seq {
					t.Errorf("%s: Sequence = %d, want %d", label, got.Sequence, tc.seq)
				}
			}

			assertSnowflakeMatches("DecodeSnowflakeRaw", decoded)

			// Forward: uint64 -> decoded fields
			decodedU64, err := DecodeSnowflakeUint64(tc.hexValue)
			if err != nil {
				t.Fatalf("DecodeSnowflakeUint64(0x%X) unexpected error: %v", tc.hexValue, err)
			}
			assertSnowflakeMatches("DecodeSnowflakeUint64", decodedU64)

			// Reverse: deltaMS + components -> encoded ID
			encodedDelta, err := EncodeSnowflake(tc.deltaMS, tc.dc, tc.worker, tc.seq)
			if err != nil {
				t.Fatalf("EncodeSnowflake unexpected error: %v", err)
			}
			assertSnowflakeMatches("EncodeSnowflake", encodedDelta)

			// Reverse: UTC timestamp ms + components -> encoded ID
			encodedTS, err := EncodeSnowflakeFromTimestamp(tc.tsMS, tc.dc, tc.worker, tc.seq)
			if err != nil {
				t.Fatalf("EncodeSnowflakeFromTimestamp unexpected error: %v", err)
			}
			assertSnowflakeMatches("EncodeSnowflakeFromTimestamp", encodedTS)

			// Strict ParseSnowflakeID rejects 0 (SV-1) and accepts SV-2..SV-4.
			parsed, err := ParseSnowflakeID(tc.id)
			if tc.hexValue == 0 {
				if !errors.Is(err, ErrInvalidSnowflake) {
					t.Fatalf("ParseSnowflakeID(%q) error = %v, want ErrInvalidSnowflake", tc.id, err)
				}
			} else {
				if err != nil {
					t.Fatalf("ParseSnowflakeID(%q) unexpected error: %v", tc.id, err)
				}
				assertSnowflakeMatches("ParseSnowflakeID", parsed)
			}
		})
	}
}

func TestParseSnowflakePreSnowflakeBoundary(t *testing.T) {
	preIDs := []struct {
		raw  string
		want string
		val  uint64
	}{
		{"1", "1", 1},
		{"00020", "20", 20},
		{"4194303", "4194303", PreSnowflakeThreshold - 1},
	}

	for _, tc := range preIDs {
		got, err := ParseSnowflakeID(tc.raw)
		if err != nil {
			t.Fatalf("ParseSnowflakeID(%q) unexpected error: %v", tc.raw, err)
		}
		if got.ID != tc.want || got.Value != tc.val {
			t.Errorf("ParseSnowflakeID(%q) = (%q, %d), want (%q, %d)", tc.raw, got.ID, got.Value, tc.want, tc.val)
		}
		if got.IsSnowflake {
			t.Errorf("ParseSnowflakeID(%q).IsSnowflake = true, want false", tc.raw)
		}
		if got.TimestampMS != 0 || !got.Timestamp.IsZero() {
			t.Errorf("ParseSnowflakeID(%q) populated timestamp (%d, %v), want zero", tc.raw, got.TimestampMS, got.Timestamp)
		}
	}

	// Exact first Snowflake ID: 2^22 = 4194304.
	first, err := ParseSnowflakeID("4194304")
	if err != nil {
		t.Fatalf("ParseSnowflakeID(4194304) unexpected error: %v", err)
	}
	if !first.IsSnowflake {
		t.Fatalf("ParseSnowflakeID(4194304).IsSnowflake = false, want true")
	}
	if first.DeltaMS != 1 || first.TimestampMS != TwepochMS+1 {
		t.Fatalf("ParseSnowflakeID(4194304) delta=%d ts=%d, want 1 and %d", first.DeltaMS, first.TimestampMS, TwepochMS+1)
	}
	if wantTS := time.UnixMilli(int64(TwepochMS + 1)).UTC(); !first.Timestamp.Equal(wantTS) {
		t.Fatalf("ParseSnowflakeID(4194304).Timestamp = %v, want %v", first.Timestamp, wantTS)
	}
}

func TestSnowflakeRejectsInvalidAndOver63Bit(t *testing.T) {
	invalid := []string{
		"",
		"0",
		"0000",
		"-1",
		"-1791257321273778509",
		"+1791257321273778509",
		" 1791257321273778509",
		"1791257321273778509 ",
		"179125732127377850a",
		"1_000",
		"1.5",
		"9223372036854775808",  // 2^63 (bit 63 set)
		"18446744073709551615", // 2^64 - 1 (MaxUint64)
		"18446744073709551616", // 2^64 (overflow uint64)
		"99999999999999999999",
	}

	for _, raw := range invalid {
		if _, err := ParseSnowflakeID(raw); !errors.Is(err, ErrInvalidSnowflake) {
			t.Errorf("ParseSnowflakeID(%q) error = %v, want ErrInvalidSnowflake", raw, err)
		}
	}

	// Bit 63 set rejected by DecodeSnowflakeRaw, DecodeSnowflakeUint64, and FormatSnowflakeID.
	over63 := uint64(math.MaxInt64) + 1
	if _, err := DecodeSnowflakeUint64(over63); !errors.Is(err, ErrInvalidSnowflake) {
		t.Errorf("DecodeSnowflakeUint64(2^63) error = %v, want ErrInvalidSnowflake", err)
	}
	if _, err := DecodeSnowflakeRaw(strconv.FormatUint(over63, 10)); !errors.Is(err, ErrInvalidSnowflake) {
		t.Errorf("DecodeSnowflakeRaw(2^63) error = %v, want ErrInvalidSnowflake", err)
	}
	if _, err := FormatSnowflakeID(0); !errors.Is(err, ErrInvalidSnowflake) {
		t.Errorf("FormatSnowflakeID(0) error = %v, want ErrInvalidSnowflake", err)
	}
	if _, err := FormatSnowflakeID(over63); !errors.Is(err, ErrInvalidSnowflake) {
		t.Errorf("FormatSnowflakeID(2^63) error = %v, want ErrInvalidSnowflake", err)
	}

	// Out-of-range encoder components rejected.
	if _, err := EncodeSnowflake(maxSnowflakeDeltaMS+1, 0, 0, 0); !errors.Is(err, ErrInvalidSnowflake) {
		t.Errorf("EncodeSnowflake(delta overflow) error = %v, want ErrInvalidSnowflake", err)
	}
	if _, err := EncodeSnowflake(0, 32, 0, 0); !errors.Is(err, ErrInvalidSnowflake) {
		t.Errorf("EncodeSnowflake(dc overflow) error = %v, want ErrInvalidSnowflake", err)
	}
	if _, err := EncodeSnowflake(0, 0, 32, 0); !errors.Is(err, ErrInvalidSnowflake) {
		t.Errorf("EncodeSnowflake(worker overflow) error = %v, want ErrInvalidSnowflake", err)
	}
	if _, err := EncodeSnowflake(0, 0, 0, 4096); !errors.Is(err, ErrInvalidSnowflake) {
		t.Errorf("EncodeSnowflake(seq overflow) error = %v, want ErrInvalidSnowflake", err)
	}
	if _, err := EncodeSnowflakeFromTimestamp(TwepochMS-1, 0, 0, 0); !errors.Is(err, ErrInvalidSnowflake) {
		t.Errorf("EncodeSnowflakeFromTimestamp(before epoch) error = %v, want ErrInvalidSnowflake", err)
	}
}

func TestSnowflakeExactIntegerComparisonWithoutFloat64Loss(t *testing.T) {
	// Adjacent 19-digit IDs that collapse to identical float64 values.
	a := "1791257321273778509"
	b := "1791257321273778510"
	maxMinusOne := "9223372036854775806"
	max63 := "9223372036854775807"

	// Sanity check that float64 indeed cannot distinguish maxMinusOne and max63.
	if float64(math.MaxInt64-1) != float64(math.MaxInt64) {
		t.Fatalf("unexpected float64 precision")
	}

	cases := []struct {
		left, right string
		want        int
	}{
		{a, b, -1},
		{b, a, 1},
		{a, a, 0},
		{"000" + a, a, 0},
		{"20", a, -1},
		{maxMinusOne, max63, -1},
		{max63, maxMinusOne, 1},
		{max63, max63, 0},
	}

	for _, tc := range cases {
		got, err := CompareSnowflakeID(tc.left, tc.right)
		if err != nil {
			t.Fatalf("CompareSnowflakeID(%q, %q) unexpected error: %v", tc.left, tc.right, err)
		}
		if got != tc.want {
			t.Errorf("CompareSnowflakeID(%q, %q) = %d, want %d", tc.left, tc.right, got, tc.want)
		}
	}

	// CompareUint64ID handles full 64-bit unsigned range cleanly.
	uMaxMinusOne := fmt.Sprintf("%d", uint64(math.MaxUint64-1))
	uMax := fmt.Sprintf("%d", uint64(math.MaxUint64))
	cmp, err := CompareUint64ID(uMaxMinusOne, uMax)
	if err != nil || cmp != -1 {
		t.Fatalf("CompareUint64ID(%s, %s) = (%d, %v), want (-1, nil)", uMaxMinusOne, uMax, cmp, err)
	}

	// FormatSnowflakeID preserves exact string for MaxSnowflakeID.
	formatted, err := FormatSnowflakeID(MaxSnowflakeID)
	if err != nil || formatted != max63 {
		t.Fatalf("FormatSnowflakeID(MaxSnowflakeID) = (%q, %v), want (%q, nil)", formatted, err, max63)
	}
}
