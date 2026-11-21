package weibo

import (
	"errors"
	"testing"
)

func TestBase62MidMblogIDVectors(t *testing.T) {
	// All 7 positive vectors from docs/research/weibo.md §3.2.
	vectors := []struct {
		name    string
		mid     string
		mblogid string
	}{
		{name: "zero_middle_and_min_tail", mid: "5000000000000001", mblogid: "O00000001"},
		{name: "left_padding_middle_and_tail", mid: "5100123450000062", mblogid: "P03d70010"},
		{name: "max_seven_digit_chunks", mid: "4999999999999999", mblogid: "NFXsjFXsj"},
		{name: "two_char_leading_chunk_ten_total", mid: "6200000010000001", mblogid: "1000010001"},
		{name: "leading_zero_middle_full_tail", mid: "3501234567890123", mblogid: "z0w7ex6A3"},
		{name: "standard_three_chunk_synthetic", mid: "5209876543210009", mblogid: "Q48VUdt4l"},
		{name: "historical_single_chunk", mid: "1234567", mblogid: "5ban"},
	}

	for _, tc := range vectors {
		t.Run(tc.name, func(t *testing.T) {
			gotMblogID, err := MidToMblogID(tc.mid)
			if err != nil {
				t.Fatalf("MidToMblogID(%q) unexpected error: %v", tc.mid, err)
			}
			if gotMblogID != tc.mblogid {
				t.Fatalf("MidToMblogID(%q) = %q, want %q", tc.mid, gotMblogID, tc.mblogid)
			}

			gotMid, err := MblogIDToMid(tc.mblogid)
			if err != nil {
				t.Fatalf("MblogIDToMid(%q) unexpected error: %v", tc.mblogid, err)
			}
			if gotMid != tc.mid {
				t.Fatalf("MblogIDToMid(%q) = %q, want %q", tc.mblogid, gotMid, tc.mid)
			}
		})
	}
}

func TestBase62MidPrecisionBeyondFloat64(t *testing.T) {
	// 19-digit mid strictly beyond 2^53 (9007199254740992) must round-trip losslessly.
	const highMid = "9007199254740993123"
	mblogid, err := MidToMblogID(highMid)
	if err != nil {
		t.Fatalf("MidToMblogID(%q) error: %v", highMid, err)
	}
	roundTrip, err := MblogIDToMid(mblogid)
	if err != nil {
		t.Fatalf("MblogIDToMid(%q) error: %v", mblogid, err)
	}
	if roundTrip != highMid {
		t.Fatalf("round-trip precision lost: got %q, want %q", roundTrip, highMid)
	}

	// Leading zeros on mid are stripped during normalization.
	got, err := MidToMblogID("0005000000000000001")
	if err != nil || got != "O00000001" {
		t.Fatalf("MidToMblogID with leading zeros = (%q, %v), want (O00000001, nil)", got, err)
	}
}

func TestBase62RejectsInvalidInputs(t *testing.T) {
	// Vector #8 from weibo.md §3.2 plus additional invalid cases.
	badMblogIDs := []struct {
		name string
		in   string
	}{
		{name: "non_first_chunk_overflow_FXsk", in: "OFXsk0001"},
		{name: "tail_chunk_overflow_ZZZZ", in: "O0000ZZZZ"},
		{name: "empty", in: ""},
		{name: "all_zero_single", in: "0"},
		{name: "all_zero_multi", in: "000000000"},
		{name: "invalid_dash", in: "O000-0001"},
		{name: "invalid_underscore", in: "O000_0001"},
		{name: "invalid_space", in: "O000 0001"},
		{name: "invalid_unicode", in: "O0000微博"},
	}
	for _, tc := range badMblogIDs {
		t.Run("decode_"+tc.name, func(t *testing.T) {
			if got, err := MblogIDToMid(tc.in); err == nil {
				t.Fatalf("MblogIDToMid(%q) succeeded with %q, want error", tc.in, got)
			} else if !errors.Is(err, ErrInvalidMblogID) {
				t.Fatalf("MblogIDToMid(%q) error %v does not wrap ErrInvalidMblogID", tc.in, err)
			}
		})
	}

	badMids := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "zero", in: "0"},
		{name: "all_zeros", in: "0000000000000000"},
		{name: "negative", in: "-5000000000000001"},
		{name: "letters", in: "500000000000000a"},
		{name: "whitespace", in: " 5000000000000001"},
		{name: "decimal_point", in: "5000000000000001.0"},
	}
	for _, tc := range badMids {
		t.Run("encode_"+tc.name, func(t *testing.T) {
			if got, err := MidToMblogID(tc.in); err == nil {
				t.Fatalf("MidToMblogID(%q) succeeded with %q, want error", tc.in, got)
			} else if !errors.Is(err, ErrInvalidMid) {
				t.Fatalf("MidToMblogID(%q) error %v does not wrap ErrInvalidMid", tc.in, err)
			}
		})
	}
}
