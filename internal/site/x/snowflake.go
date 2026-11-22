package x

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
)

// SiteID is the canonical site identifier for X (formerly Twitter).
// Defined locally as site.ID("x") so core algorithmic helpers stay decoupled
// from cross-package site registry wiring.
const SiteID site.ID = "x"

const (
	// TwepochMS is the Twitter Snowflake epoch in UTC milliseconds (2010-11-04T01:42:54.657Z).
	TwepochMS uint64 = 1288834974657

	// PreSnowflakeThreshold is 2^22 (4194304). Valid positive IDs strictly below
	// this threshold have delta_ms == 0 and represent pre-Snowflake sequential IDs.
	PreSnowflakeThreshold uint64 = 1 << 22

	// MaxSnowflakeID is the 63-bit signed positive upper bound (0x7FFFFFFFFFFFFFFF).
	// Twitter Snowflakes always keep bit 63 clear.
	MaxSnowflakeID uint64 = math.MaxInt64

	snowflakeTimestampShift = 22
	snowflakeDatacenterBits = 5
	snowflakeWorkerBits     = 5
	snowflakeSequenceBits   = 12

	snowflakeDatacenterShift = snowflakeWorkerBits + snowflakeSequenceBits
	snowflakeWorkerShift     = snowflakeSequenceBits

	maxSnowflakeDeltaMS    uint64 = (1 << 41) - 1
	maxSnowflakeDatacenter uint8  = (1 << snowflakeDatacenterBits) - 1
	maxSnowflakeWorker     uint8  = (1 << snowflakeWorkerBits) - 1
	maxSnowflakeSequence   uint16 = (1 << snowflakeSequenceBits) - 1
)

// ErrInvalidSnowflake is returned when an X ID string or numeric value violates
// decimal syntax, sign, range, or bit-width constraints.
var ErrInvalidSnowflake = errors.New("x: invalid snowflake id")

// Snowflake holds the exact 64-bit integer, canonical decimal string, and
// decoded bit fields of an X Snowflake or pre-Snowflake ID.
// All arithmetic uses exact uint64 and decimal string operations (never float64).
type Snowflake struct {
	ID           string    // canonical base-10 string with leading zeros stripped
	Value        uint64    // exact unsigned 64-bit integer value (bit 63 == 0)
	IsSnowflake  bool      // true when Value >= PreSnowflakeThreshold (2^22)
	DeltaMS      uint64    // 41-bit millisecond offset from TwepochMS (0 when !IsSnowflake in ParseSnowflakeID)
	TimestampMS  uint64    // TwepochMS + DeltaMS (0 when !IsSnowflake in ParseSnowflakeID)
	Timestamp    time.Time // UTC timestamp derived from TimestampMS (zero when !IsSnowflake in ParseSnowflakeID)
	DatacenterID uint8     // 5-bit datacenter id (0..31)
	WorkerID     uint8     // 5-bit worker id (0..31)
	Sequence     uint16    // 12-bit per-millisecond sequence (0..4095)
}

// ParseSnowflakeID validates an X status or user ID string as a positive 63-bit
// decimal integer (1..MaxSnowflakeID) and decodes Snowflake fields when Value >= 2^22.
//
// Pre-Snowflake sequential IDs (1 <= Value < 2^22) are accepted with IsSnowflake == false
// and zero timestamp fields so callers do not confuse them with 2010-11-04 timestamps.
// Zero, negative, non-numeric, and >63-bit values (MSB bit 63 set) are rejected.
func ParseSnowflakeID(raw string) (Snowflake, error) {
	v, canonical, err := parseStrictDecimalUint64(raw, false)
	if err != nil {
		return Snowflake{}, err
	}
	if v > MaxSnowflakeID {
		return Snowflake{}, fmt.Errorf("%w: exceeds 63-bit signed limit", ErrInvalidSnowflake)
	}
	if v < PreSnowflakeThreshold {
		return Snowflake{
			ID:          canonical,
			Value:       v,
			IsSnowflake: false,
		}, nil
	}
	return decodeSnowflakeBitsUnchecked(v, canonical), nil
}

// DecodeSnowflakeRaw decodes any 63-bit non-negative decimal integer string
// ("0".."9223372036854775807") into its raw Twitter Snowflake bit fields.
// Unlike ParseSnowflakeID, this accepts "0" (epoch lower bound SV-1) and always
// populates TimestampMS/Timestamp/DatacenterID/WorkerID/Sequence from the bit layout.
func DecodeSnowflakeRaw(raw string) (Snowflake, error) {
	v, canonical, err := parseStrictDecimalUint64(raw, true)
	if err != nil {
		return Snowflake{}, err
	}
	if v > MaxSnowflakeID {
		return Snowflake{}, fmt.Errorf("%w: exceeds 63-bit signed limit", ErrInvalidSnowflake)
	}
	return decodeSnowflakeBitsUnchecked(v, canonical), nil
}

// DecodeSnowflakeUint64 decodes a 63-bit non-negative integer into its raw
// Twitter Snowflake bit fields. Values with bit 63 set (> MaxSnowflakeID) are rejected.
func DecodeSnowflakeUint64(v uint64) (Snowflake, error) {
	if v > MaxSnowflakeID {
		return Snowflake{}, fmt.Errorf("%w: exceeds 63-bit signed limit", ErrInvalidSnowflake)
	}
	return decodeSnowflakeBitsUnchecked(v, strconv.FormatUint(v, 10)), nil
}

// EncodeSnowflake packs Twitter Snowflake bit fields into a 63-bit value and
// canonical decimal string.
func EncodeSnowflake(deltaMS uint64, datacenterID, workerID uint8, sequence uint16) (Snowflake, error) {
	if deltaMS > maxSnowflakeDeltaMS {
		return Snowflake{}, fmt.Errorf("%w: delta_ms out of 41-bit range", ErrInvalidSnowflake)
	}
	if datacenterID > maxSnowflakeDatacenter {
		return Snowflake{}, fmt.Errorf("%w: datacenter_id out of 5-bit range", ErrInvalidSnowflake)
	}
	if workerID > maxSnowflakeWorker {
		return Snowflake{}, fmt.Errorf("%w: worker_id out of 5-bit range", ErrInvalidSnowflake)
	}
	if sequence > maxSnowflakeSequence {
		return Snowflake{}, fmt.Errorf("%w: sequence out of 12-bit range", ErrInvalidSnowflake)
	}
	v := (deltaMS << snowflakeTimestampShift) |
		(uint64(datacenterID) << snowflakeDatacenterShift) |
		(uint64(workerID) << snowflakeWorkerShift) |
		uint64(sequence)
	return decodeSnowflakeBitsUnchecked(v, strconv.FormatUint(v, 10)), nil
}

// EncodeSnowflakeFromTimestamp encodes a UTC millisecond timestamp (>= TwepochMS)
// together with datacenter, worker, and sequence fields into a Snowflake.
func EncodeSnowflakeFromTimestamp(tsMS uint64, datacenterID, workerID uint8, sequence uint16) (Snowflake, error) {
	if tsMS < TwepochMS {
		return Snowflake{}, fmt.Errorf("%w: timestamp precedes twitter epoch", ErrInvalidSnowflake)
	}
	return EncodeSnowflake(tsMS-TwepochMS, datacenterID, workerID, sequence)
}

// FormatSnowflakeID validates that v is a positive 63-bit ID (1..MaxSnowflakeID)
// and formats it as a base-10 string without passing through floating-point.
func FormatSnowflakeID(v uint64) (string, error) {
	if v == 0 {
		return "", fmt.Errorf("%w: zero value", ErrInvalidSnowflake)
	}
	if v > MaxSnowflakeID {
		return "", fmt.Errorf("%w: exceeds 63-bit signed limit", ErrInvalidSnowflake)
	}
	return strconv.FormatUint(v, 10), nil
}

// CompareSnowflakeID compares two valid positive 63-bit X ID strings using exact
// 64-bit integer order. Returns -1 if a < b, 0 if a == b, and +1 if a > b.
func CompareSnowflakeID(a, b string) (int, error) {
	sa, err := ParseSnowflakeID(a)
	if err != nil {
		return 0, err
	}
	sb, err := ParseSnowflakeID(b)
	if err != nil {
		return 0, err
	}
	switch {
	case sa.Value < sb.Value:
		return -1, nil
	case sa.Value > sb.Value:
		return 1, nil
	default:
		return 0, nil
	}
}

// CompareUint64ID compares two positive unsigned 64-bit decimal ID strings
// (1..math.MaxUint64) using exact uint64 order (never float64).
func CompareUint64ID(a, b string) (int, error) {
	va, _, err := parseStrictDecimalUint64(a, false)
	if err != nil {
		return 0, err
	}
	vb, _, err := parseStrictDecimalUint64(b, false)
	if err != nil {
		return 0, err
	}
	switch {
	case va < vb:
		return -1, nil
	case va > vb:
		return 1, nil
	default:
		return 0, nil
	}
}

func decodeSnowflakeBitsUnchecked(v uint64, canonical string) Snowflake {
	deltaMS := v >> snowflakeTimestampShift
	tsMS := TwepochMS + deltaMS
	return Snowflake{
		ID:           canonical,
		Value:        v,
		IsSnowflake:  v >= PreSnowflakeThreshold,
		DeltaMS:      deltaMS,
		TimestampMS:  tsMS,
		Timestamp:    time.UnixMilli(int64(tsMS)).UTC(),
		DatacenterID: uint8((v >> snowflakeDatacenterShift) & uint64(maxSnowflakeDatacenter)),
		WorkerID:     uint8((v >> snowflakeWorkerShift) & uint64(maxSnowflakeWorker)),
		Sequence:     uint16(v & uint64(maxSnowflakeSequence)),
	}
}

func parseStrictDecimalUint64(raw string, allowZero bool) (uint64, string, error) {
	if raw == "" {
		return 0, "", fmt.Errorf("%w: empty string", ErrInvalidSnowflake)
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, "", fmt.Errorf("%w: non-digit character", ErrInvalidSnowflake)
		}
	}
	canonical := strings.TrimLeft(raw, "0")
	if canonical == "" {
		if !allowZero {
			return 0, "", fmt.Errorf("%w: zero value", ErrInvalidSnowflake)
		}
		return 0, "0", nil
	}
	v, err := strconv.ParseUint(canonical, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("%w: integer overflow", ErrInvalidSnowflake)
	}
	return v, canonical, nil
}
