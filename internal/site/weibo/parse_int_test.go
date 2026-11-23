package weibo

import (
	"encoding/json"
	"math"
	"testing"
)

func TestParseJSONIntStaysWithin32Bits(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{`12`, 12},
		{`"34"`, 34},
		{`-5`, -5},
		{`"7.9"`, 7},
		{`""`, 0},
		{`null`, 0},
		{`9223372036854775807`, math.MaxInt32},
		{`"-9223372036854775808"`, math.MinInt32},
		{`2147483648`, math.MaxInt32},
	}
	for _, c := range cases {
		if got := parseJSONInt(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("parseJSONInt(%s) = %d, want %d", c.raw, got, c.want)
		}
	}
}
