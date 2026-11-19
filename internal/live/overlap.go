package live

import "github.com/Eyevinn/mp4ff/mp4"

// trimFragmentOverlaps prepares a recorded fMP4 for mp4.Defragment.
//
// Consecutive HLS fMP4 segments of a live stream sometimes declare a tfdt a
// little before the end of the previous segment of the same track: Bilibili's
// AAC track was seen starting 32 ticks (under 1 ms at 48 kHz) inside the last
// frame of the previous segment. Defragment treats a later tfdt as a
// retransmission and drops superseded samples, but only on sample boundaries;
// a cut inside a sample is rejected and the whole recording stayed a ".part".
//
// When the next fragment starts strictly inside the previous fragment's last
// sample, that sample is shortened to end where the next fragment starts. No
// sample data is dropped and later fragments keep their own decode times, so
// the timeline does not drift. Overlaps of a whole sample or more are left to
// Defragment, which resolves them on sample boundaries. It returns the number
// of samples it shortened.
func trimFragmentOverlaps(f *mp4.File) int {
	if f == nil || f.Moov == nil {
		return 0
	}
	trex := map[uint32]*mp4.TrexBox{}
	if f.Moov.Mvex != nil {
		for _, t := range f.Moov.Mvex.Trexs {
			trex[t.TrackID] = t
		}
	}
	type trackEnd struct {
		end  uint64
		last *mp4.Sample
	}
	ends := map[uint32]*trackEnd{}
	trimmed := 0
	for _, seg := range f.Segments {
		for _, frag := range seg.Fragments {
			if frag == nil || frag.Moof == nil {
				continue
			}
			for _, traf := range frag.Moof.Trafs {
				if traf == nil || traf.Tfhd == nil {
					continue
				}
				id := traf.Tfhd.TrackID
				te := ends[id]
				if te == nil {
					te = &trackEnd{}
					ends[id] = te
				}
				if traf.Tfdt != nil {
					start := traf.Tfdt.BaseMediaDecodeTime()
					if te.last != nil && start < te.end && te.end-start < uint64(te.last.Dur) {
						te.last.Dur -= uint32(te.end - start)
						trimmed++
					}
					te.end = start
					te.last = nil
				}
				for _, trun := range traf.Truns {
					// Make every duration explicit so Defragment keeps a trimmed value.
					trun.AddSampleDefaultValues(traf.Tfhd, trex[id])
					trun.Flags |= mp4.TrunSampleDurationPresentFlag
					for i := range trun.Samples {
						te.end += uint64(trun.Samples[i].Dur)
						te.last = &trun.Samples[i]
					}
				}
			}
		}
	}
	return trimmed
}

// fragmentTimelineSec returns the combined cross-track timeline of a recorded
// fMP4 in seconds: max_i(end_i/ts_i) - min_i(first_i/ts_i), matching how
// mp4.Defragment computes mvhd.Duration across tracks with differing start
// offsets. Run it after trimFragmentOverlaps, which makes every sample duration
// explicit. It returns 0 when the file has no usable decode times.
func fragmentTimelineSec(f *mp4.File) float64 {
	if f == nil || f.Moov == nil {
		return 0
	}
	timescale := map[uint32]uint32{}
	for _, trak := range f.Moov.Traks {
		if trak.Tkhd != nil && trak.Mdia != nil && trak.Mdia.Mdhd != nil {
			timescale[trak.Tkhd.TrackID] = trak.Mdia.Mdhd.Timescale
		}
	}
	type span struct {
		first uint64
		end   uint64
		has   bool
	}
	spans := map[uint32]*span{}
	for _, seg := range f.Segments {
		for _, frag := range seg.Fragments {
			if frag == nil || frag.Moof == nil {
				continue
			}
			for _, traf := range frag.Moof.Trafs {
				if traf == nil || traf.Tfhd == nil || traf.Tfdt == nil {
					continue
				}
				id := traf.Tfhd.TrackID
				start := traf.Tfdt.BaseMediaDecodeTime()
				end := start
				for _, trun := range traf.Truns {
					end += trun.Duration(0)
				}
				if end <= start {
					continue
				}
				s := spans[id]
				if s == nil {
					s = &span{first: start, end: end, has: true}
					spans[id] = s
					continue
				}
				if start < s.first {
					s.first = start
				}
				if end > s.end {
					s.end = end
				}
			}
		}
	}
	var minFirst, maxEnd float64
	found := false
	for id, s := range spans {
		if ts := timescale[id]; ts > 0 && s.has && s.end > s.first {
			firstSec := float64(s.first) / float64(ts)
			endSec := float64(s.end) / float64(ts)
			if !found {
				minFirst = firstSec
				maxEnd = endSec
				found = true
			} else {
				if firstSec < minFirst {
					minFirst = firstSec
				}
				if endSec > maxEnd {
					maxEnd = endSec
				}
			}
		}
	}
	if !found || maxEnd <= minFirst {
		return 0
	}
	return maxEnd - minFirst
}
