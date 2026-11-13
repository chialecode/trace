package live

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/library"
)

// A Bilibili HLS part converts even when the EXTINF sum the recorder kept
// differs from the media timeline (real parts drifted 0.4 s over ten minutes),
// and recovery clears the part's earlier "convert failed" record.
func TestRecoveryAcceptsEXTINFDriftAndClearsConvertFailed(t *testing.T) {
	mgr, lib, _ := regressionRecorder(t, http.NotFoundHandler())
	init, fragment := regressionLiveSamples(t)
	session := regressionLiveSession(t, lib)
	raw := append(append([]byte(nil), init...), fragment...)
	part := filepath.Join(session.itemDir, "part-001.mp4.part")
	if err := os.WriteFile(part, raw, 0600); err != nil {
		t.Fatal(err)
	}
	session.itemMeta.Live.PendingFile, session.itemMeta.Live.PendingBytes, session.itemMeta.Live.PendingDuration = "part-001.mp4.part", int64(len(raw)), 1.6
	session.itemMeta.Live.Incidents = []library.LiveIncident{
		{Kind: library.LiveIncidentConvertFailed, At: "2026-11-15T15:34:32Z", Part: "part-001.mp4.part", Count: 1, Reason: "直播录像转换失败：直播录像轨道或时长校验失败"},
		{Kind: library.LiveIncidentReconnect, At: "2026-11-15T15:40:42Z", Part: "part-002.mp4.part", Count: 1, DurationSec: 10.3},
	}
	if _, err := lib.SaveItem(session.itemDir, session.itemMeta); err != nil {
		t.Fatal(err)
	}
	mgr.RecoverOrphanParts()
	meta, _, err := lib.GetItem(session.itemMeta.ItemKey)
	if err != nil || len(meta.PendingParts) != 0 || len(meta.Live.Segments) != 1 || meta.Status != "complete" {
		t.Fatalf("drifted part did not convert: %+v %v", meta, err)
	}
	if got := meta.Live.Incidents; len(got) != 1 || got[0].Kind != library.LiveIncidentReconnect {
		t.Fatalf("convert-failed record not cleared or other records lost: %+v", got)
	}
}

// A sequence gap longer than the time since the previous segment is a restarted
// stream's new numbering, not lost segments; a plausible gap is still recorded.
func TestHLSSequenceResetIsNotLostSegments(t *testing.T) {
	for _, tc := range []struct {
		name     string
		next     string
		wantLost int
	}{
		{"restarted stream", "100000008", 0},
		{"real gap", "10", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			init, fragment := regressionLiveSamples(t)
			var hits atomic.Int32
			mgr, lib, base := regressionRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/index":
					if hits.Add(1) == 1 {
						_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:7\n#EXT-X-MAP:URI=\"/init\"\n#EXTINF:1,\n/fragment\n"))
						return
					}
					_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:" + tc.next + "\n#EXT-X-MAP:URI=\"/init\"\n#EXTINF:1,\n/fragment\n#EXT-X-ENDLIST\n"))
				case "/init":
					_, _ = w.Write(init)
				case "/fragment":
					_, _ = w.Write(fragment)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			session := regressionLiveSession(t, lib)
			_, err := mgr.recordHLSSegment(context.Background(), session, []string{base + "/index"}, filepath.Join(session.itemDir, "part-001.mp4.part"), true)
			if errors.Is(err, errSegmentRollover) {
				// A large jump closes the part; the next part starts at the new number.
				session.wroteMedia = false
				_, err = mgr.recordHLSSegment(context.Background(), session, []string{base + "/index"}, filepath.Join(session.itemDir, "part-002.mp4.part"), true)
			}
			if err != nil {
				t.Fatal(err)
			}
			lost := 0
			for _, inc := range session.incidents {
				if inc.Kind == library.LiveIncidentLostSegments {
					lost += inc.LostSegments
				}
			}
			if lost != tc.wantLost {
				t.Fatalf("lost segments = %d, want %d: %+v", lost, tc.wantLost, session.incidents)
			}
		})
	}
}
