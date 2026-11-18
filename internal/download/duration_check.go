package download

import (
	"fmt"
	"math"
)

// durationTolerance is how far a saved file's duration may be from the one
// the site states: 2 seconds, or 3% of a longer video. The site's figure is
// the upload's, while its transcoded streams can run a little shorter (dev
// 2026-11-18: a page listed at 129 s whose video and audio streams both ran
// 126.5 s). Previews are refused before downloading, and a stream cut short
// fails the length check, so this only catches output gone wrong.
func durationTolerance(expectedSec float64) float64 {
	return math.Max(2, expectedSec*0.03)
}

// durationMismatch describes how gotSec misses expectedSec, or is empty
// when it is close enough (or nothing is expected).
func durationMismatch(gotSec, expectedSec float64) string {
	if expectedSec <= 0 {
		return ""
	}
	tol := durationTolerance(expectedSec)
	if math.Abs(gotSec-expectedSec) <= tol {
		return ""
	}
	return fmt.Sprintf("MP4 时长 %.1f 秒与站点给的 %.1f 秒相差超过 %.1f 秒", gotSec, expectedSec, tol)
}
