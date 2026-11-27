package library

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Merge modes of an imported item that conflicts with the local one
// (docs/design/remote.md §6.3).
const (
	// MergeKeepLocal keeps every local file and fills in only what the
	// local item lacks.
	MergeKeepLocal = "keep_local"
	// MergeUseImported makes the imported manifest the item's manifest.
	MergeUseImported = "use_imported"
	// MergeUnion keeps local files of the same name, adds the rest and
	// joins parts, live segments and incidents.
	MergeUnion = "merge"
)

var livePartName = regexp.MustCompile(`^part-(\d+)(\.[A-Za-z0-9]+)$`)

// SegmentRename is an imported live segment stored under another name
// because the local item already has a different file with its name (two
// installations recorded the same broadcast, each numbering from 001).
type SegmentRename struct{ From, To string }

func cloneItemReplyTarget(src *ItemReplyTarget) *ItemReplyTarget {
	if src == nil {
		return nil
	}
	cp := *src
	return &cp
}

func cloneItemArticle(src *ItemArticle) *ItemArticle {
	if src == nil {
		return nil
	}
	cp := *src
	if len(src.Blocks) > 0 {
		cp.Blocks = make([]ItemArticleBlock, len(src.Blocks))
		for i, blk := range src.Blocks {
			blk.Links = append([]ItemLink(nil), blk.Links...)
			cp.Blocks[i] = blk
		}
	}
	return &cp
}

func mergeItemArticle(dst, src *ItemArticle) *ItemArticle {
	if src == nil {
		return dst
	}
	if dst == nil {
		return cloneItemArticle(src)
	}
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.CoverRel == "" {
		dst.CoverRel = src.CoverRel
	}
	if dst.PlainText == "" {
		dst.PlainText = src.PlainText
	}
	if len(dst.Blocks) == 0 && len(src.Blocks) > 0 {
		dst.Blocks = cloneItemArticle(src).Blocks
	} else {
		for i := 0; i < len(dst.Blocks) && i < len(src.Blocks); i++ {
			if dst.Blocks[i].Type != src.Blocks[i].Type {
				continue
			}
			if dst.Blocks[i].MediaRel == "" {
				dst.Blocks[i].MediaRel = src.Blocks[i].MediaRel
			}
			if dst.Blocks[i].TweetID == "" {
				dst.Blocks[i].TweetID = src.Blocks[i].TweetID
			}
			if len(dst.Blocks[i].Links) == 0 && len(src.Blocks[i].Links) > 0 {
				dst.Blocks[i].Links = append([]ItemLink(nil), src.Blocks[i].Links...)
			}
		}
	}
	return dst
}

func cloneItemOrigin(src *ItemOrigin) *ItemOrigin {
	return cloneItemOriginDepth(src, 0)
}

func cloneItemOriginDepth(src *ItemOrigin, depth int) *ItemOrigin {
	if src == nil {
		return nil
	}
	o := *src
	o.Links = append([]ItemLink(nil), o.Links...)
	if o.Card != nil {
		oc := *o.Card
		o.Card = &oc
	}
	o.InReplyTo = cloneItemReplyTarget(o.InReplyTo)
	if depth >= maxItemOriginNestingDepth {
		o.Origin = nil
	} else {
		o.Origin = cloneItemOriginDepth(o.Origin, depth+1)
	}
	return &o
}

func mergeItemReplyTarget(dst, src *ItemReplyTarget) *ItemReplyTarget {
	if src == nil {
		return dst
	}
	if dst == nil {
		return cloneItemReplyTarget(src)
	}
	if dst.StatusID == "" {
		dst.StatusID = src.StatusID
	}
	if dst.UserID == "" {
		dst.UserID = src.UserID
	}
	if dst.ScreenName == "" {
		dst.ScreenName = src.ScreenName
	}
	return dst
}

func mergeItemOriginDepth(dst, src *ItemOrigin, depth int) *ItemOrigin {
	if src == nil {
		if dst != nil && depth >= maxItemOriginNestingDepth {
			dst.Origin = nil
		}
		return dst
	}
	if dst == nil {
		return cloneItemOriginDepth(src, depth)
	}
	if dst.Site == "" {
		dst.Site = src.Site
	}
	if dst.ID == "" {
		dst.ID = src.ID
	}
	if dst.MblogID == "" {
		dst.MblogID = src.MblogID
	}
	if dst.CanonicalURL == "" {
		dst.CanonicalURL = src.CanonicalURL
	}
	if dst.Author.UID == "" {
		dst.Author.UID = src.Author.UID
	}
	if dst.Author.Site == "" {
		dst.Author.Site = src.Author.Site
	}
	if dst.Author.UniqueID == "" {
		dst.Author.UniqueID = src.Author.UniqueID
	}
	if dst.Author.Name == "" {
		dst.Author.Name = src.Author.Name
	}
	if dst.Author.Face == "" {
		dst.Author.Face = src.Author.Face
	}
	if dst.PubTs == 0 {
		dst.PubTs = src.PubTs
	}
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.Text == "" {
		dst.Text = src.Text
	}
	if len(dst.Links) == 0 && len(src.Links) > 0 {
		dst.Links = append([]ItemLink(nil), src.Links...)
	}
	if dst.UnavailableReason == "" {
		dst.UnavailableReason = src.UnavailableReason
	}
	if !dst.VideoFromOrigin && src.VideoFromOrigin {
		dst.VideoFromOrigin = true
	}
	if dst.Card == nil && src.Card != nil {
		oc := *src.Card
		dst.Card = &oc
	}
	dst.InReplyTo = mergeItemReplyTarget(dst.InReplyTo, src.InReplyTo)
	if depth >= maxItemOriginNestingDepth {
		dst.Origin = nil
	} else {
		dst.Origin = mergeItemOriginDepth(dst.Origin, src.Origin, depth+1)
	}
	return dst
}

func cloneItemMetadataSlices(meta ItemMetadata) ItemMetadata {
	meta.Media = append([]MediaFile(nil), meta.Media...)
	meta.Parts = append([]VideoPartRecord(nil), meta.Parts...)
	for i := range meta.Parts {
		meta.Parts[i].Subtitles = append([]string(nil), meta.Parts[i].Subtitles...)
		meta.Parts[i].SubtitleTracks = append([]PartSubtitleInfo(nil), meta.Parts[i].SubtitleTracks...)
		meta.Parts[i].Chapters = append([]VideoChapter(nil), meta.Parts[i].Chapters...)
	}
	meta.ExpectedPages = append([]int(nil), meta.ExpectedPages...)
	meta.NotePages = append([]NotePageRecord(nil), meta.NotePages...)
	for i := range meta.NotePages {
		if meta.NotePages[i].VideoInfo != nil {
			vi := *meta.NotePages[i].VideoInfo
			meta.NotePages[i].VideoInfo = &vi
		}
	}
	if meta.DouyinVideo != nil {
		dv := *meta.DouyinVideo
		meta.DouyinVideo = &dv
	}
	if meta.Music != nil {
		mu := *meta.Music
		meta.Music = &mu
	}
	if meta.CachedBy != nil {
		cb := *meta.CachedBy
		meta.CachedBy = &cb
	}
	if meta.Via != nil {
		v := *meta.Via
		meta.Via = &v
	}
	if meta.Card != nil {
		c := *meta.Card
		meta.Card = &c
	}
	meta.Article = cloneItemArticle(meta.Article)
	meta.Links = append([]ItemLink(nil), meta.Links...)
	meta.InReplyTo = cloneItemReplyTarget(meta.InReplyTo)
	meta.Origin = cloneItemOrigin(meta.Origin)
	if len(meta.Thread) > 0 {
		meta.Thread = append([]ItemThreadPart(nil), meta.Thread...)
		for i := range meta.Thread {
			meta.Thread[i].Links = append([]ItemLink(nil), meta.Thread[i].Links...)
			meta.Thread[i].MediaIndexes = append([]int(nil), meta.Thread[i].MediaIndexes...)
			meta.Thread[i].Origin = cloneItemOrigin(meta.Thread[i].Origin)
		}
	}
	if meta.Live != nil {
		lv := *meta.Live
		lv.Segments = append([]LiveSegmentInfo(nil), lv.Segments...)
		lv.Incidents = append([]LiveIncident(nil), lv.Incidents...)
		meta.Live = &lv
	}
	meta.Missing = append([]MissingRecord(nil), meta.Missing...)
	meta.PendingParts = append([]string(nil), meta.PendingParts...)
	meta.Videos = append([]string(nil), meta.Videos...)
	meta.AssetIDs = append([]string(nil), meta.AssetIDs...)
	return meta
}

// MergeItemMetadata joins an imported manifest into the local one.
// sameContent reports whether a registered path is present locally with the
// imported content (same bytes and SHA-256); paths the local item has with
// other content keep the local file except in MergeUseImported. Live
// segments of the imported item whose names are taken by different local
// files (and any following segments from that import) are renumbered
// sequentially after the local ones (the returned renames say which imported
// file goes to which name). The result keeps the local folder and cachedBy.
func MergeItemMetadata(local, imported ItemMetadata, mode string, sameContent func(rel string) bool) (ItemMetadata, []SegmentRename) {
	if mode == MergeUseImported {
		out := cloneItemMetadataSlices(imported)
		out.ItemDir, out.ItemKey, out.RawID = local.ItemDir, local.ItemKey, local.RawID
		if out.ArchivedOf == "" && local.ArchivedOf != "" {
			out.ArchivedOf = local.ArchivedOf
		}
		if local.CachedBy != nil {
			cb := *local.CachedBy
			out.CachedBy = &cb
		}
		out.Missing = nil
		sanitizeManifestConflictFields(&out)
		return out, nil
	}
	out := cloneItemMetadataSlices(local)
	out.Missing = nil

	// Track imported live segment paths so mergeLive owns all live-segment
	// deduplication and sequential renumbering before updating out.Media.
	importedLiveSegPaths := map[string]bool{}
	if out.Live != nil && imported.Live != nil {
		for _, seg := range imported.Live.Segments {
			if norm := strings.ToLower(cleanManifestRel(seg.File)); norm != "" {
				importedLiveSegPaths[norm] = true
			}
		}
	}

	// Media: by path; local wins (live segments merged below when both have Live).
	have := map[string]int{}
	for i, m := range out.Media {
		have[strings.ToLower(cleanManifestRel(m.Path))] = i
	}
	for _, m := range imported.Media {
		key := strings.ToLower(cleanManifestRel(m.Path))
		if importedLiveSegPaths[key] {
			continue
		}
		if idx, exists := have[key]; !exists {
			have[key] = len(out.Media)
			out.Media = append(out.Media, m)
		} else {
			if !out.Media[idx].FromOrigin && m.FromOrigin {
				out.Media[idx].FromOrigin = true
			}
			if !out.Media[idx].GIF && m.GIF {
				out.Media[idx].GIF = true
			}
		}
	}

	// Video parts: by page; local wins, filling empty supplemented fields.
	pageIdx := map[int]int{}
	for i, p := range out.Parts {
		pageIdx[p.Page] = i
	}
	for _, p := range imported.Parts {
		if idx, exists := pageIdx[p.Page]; !exists {
			p.Subtitles = append([]string(nil), p.Subtitles...)
			p.SubtitleTracks = append([]PartSubtitleInfo(nil), p.SubtitleTracks...)
			p.Chapters = append([]VideoChapter(nil), p.Chapters...)
			pageIdx[p.Page] = len(out.Parts)
			out.Parts = append(out.Parts, p)
		} else {
			cur := &out.Parts[idx]
			if cur.File == "" {
				cur.File = p.File
			}
			if cur.Danmaku == "" {
				cur.Danmaku = p.Danmaku
			}
			if cur.Audio.SavedAs == "" && p.Audio.SavedAs != "" {
				cur.Audio.SavedAs = p.Audio.SavedAs
			}
			if len(cur.Chapters) == 0 && len(p.Chapters) > 0 {
				cur.Chapters = append([]VideoChapter(nil), p.Chapters...)
			}
			if len(p.Subtitles) > 0 {
				subSeen := map[string]bool{}
				for _, s := range cur.Subtitles {
					subSeen[strings.ToLower(cleanManifestRel(s))] = true
				}
				for _, s := range p.Subtitles {
					k := strings.ToLower(cleanManifestRel(s))
					if k != "" && !subSeen[k] {
						subSeen[k] = true
						cur.Subtitles = append(cur.Subtitles, s)
					}
				}
			}
		}
	}
	sort.SliceStable(out.Parts, func(i, j int) bool { return out.Parts[i].Page < out.Parts[j].Page })

	for _, pg := range imported.ExpectedPages {
		if !containsInt(out.ExpectedPages, pg) {
			out.ExpectedPages = append(out.ExpectedPages, pg)
		}
	}
	sort.Ints(out.ExpectedPages)

	// NotePages: supplement empty fields on existing pages, then append any extra pages.
	for i := 0; i < len(out.NotePages) && i < len(imported.NotePages); i++ {
		dst, src := &out.NotePages[i], imported.NotePages[i]
		if dst.MediaID == "" {
			dst.MediaID = src.MediaID
		}
		if dst.Image == "" {
			dst.Image = src.Image
		}
		if dst.NoWatermark == "" {
			dst.NoWatermark = src.NoWatermark
		}
		if dst.Cover == "" {
			dst.Cover = src.Cover
		}
		if dst.Motion == "" {
			dst.Motion = src.Motion
		}
		if dst.Video == "" {
			dst.Video = src.Video
		}
		if !dst.FromOrigin && src.FromOrigin {
			dst.FromOrigin = true
		}
		if !dst.GIF && src.GIF {
			dst.GIF = true
		}
		if dst.VideoInfo == nil && src.VideoInfo != nil {
			vi := *src.VideoInfo
			dst.VideoInfo = &vi
		} else if dst.VideoInfo != nil && src.VideoInfo != nil && dst.VideoInfo.MediaID == "" {
			dst.VideoInfo.MediaID = src.VideoInfo.MediaID
		}
	}
	if len(out.NotePages) < len(imported.NotePages) {
		for _, np := range imported.NotePages[len(out.NotePages):] {
			if np.VideoInfo != nil {
				vi := *np.VideoInfo
				np.VideoInfo = &vi
			}
			out.NotePages = append(out.NotePages, np)
		}
	}

	if out.DouyinVideo == nil && imported.DouyinVideo != nil {
		dv := *imported.DouyinVideo
		out.DouyinVideo = &dv
	} else if out.DouyinVideo != nil && imported.DouyinVideo != nil && out.DouyinVideo.MediaID == "" {
		out.DouyinVideo.MediaID = imported.DouyinVideo.MediaID
	}
	if imported.CacheStandardVersion > out.CacheStandardVersion {
		out.CacheStandardVersion = imported.CacheStandardVersion
	}
	if out.RecheckedAt == "" && imported.RecheckedAt != "" {
		out.RecheckedAt = imported.RecheckedAt
	}
	if out.EditTs == 0 && imported.EditTs > 0 {
		out.EditTs = imported.EditTs
	}
	if out.EditCount == 0 && imported.EditCount > 0 {
		out.EditCount = imported.EditCount
	}
	if len(out.AssetIDs) == 0 && len(imported.AssetIDs) > 0 {
		out.AssetIDs = append([]string(nil), imported.AssetIDs...)
	}
	if out.ArchivedOf == "" && imported.ArchivedOf != "" {
		out.ArchivedOf = imported.ArchivedOf
	}
	if out.Music == nil && imported.Music != nil {
		mu := *imported.Music
		out.Music = &mu
	} else if out.Music != nil && imported.Music != nil {
		if out.Music.File == "" {
			out.Music.File = imported.Music.File
		}
		if out.Music.Cover == "" {
			out.Music.Cover = imported.Music.Cover
		}
		if out.Music.Title == "" {
			out.Music.Title = imported.Music.Title
		}
		if out.Music.Author == "" {
			out.Music.Author = imported.Music.Author
		}
	}
	if out.CachedBy == nil && imported.CachedBy != nil {
		cb := *imported.CachedBy
		out.CachedBy = &cb
	}
	if out.Via == nil && imported.Via != nil {
		v := *imported.Via
		out.Via = &v
	}
	if out.Card == nil && imported.Card != nil {
		c := *imported.Card
		out.Card = &c
	}
	out.Article = mergeItemArticle(out.Article, imported.Article)
	if len(out.Links) == 0 && len(imported.Links) > 0 {
		out.Links = append([]ItemLink(nil), imported.Links...)
	}
	out.InReplyTo = mergeItemReplyTarget(out.InReplyTo, imported.InReplyTo)
	if len(out.Thread) == 0 && len(imported.Thread) > 0 {
		out.Thread = append([]ItemThreadPart(nil), imported.Thread...)
		for i := range out.Thread {
			out.Thread[i].Links = append([]ItemLink(nil), out.Thread[i].Links...)
			out.Thread[i].MediaIndexes = append([]int(nil), out.Thread[i].MediaIndexes...)
			out.Thread[i].Origin = cloneItemOrigin(out.Thread[i].Origin)
		}
	} else if len(out.Thread) > 0 && len(imported.Thread) > 0 {
		byID := make(map[string]ItemThreadPart, len(imported.Thread))
		for _, impPart := range imported.Thread {
			if id := strings.TrimSpace(impPart.ID); id != "" {
				byID[id] = impPart
			}
		}
		for i := range out.Thread {
			impPart, ok := byID[strings.TrimSpace(out.Thread[i].ID)]
			if !ok {
				continue
			}
			if len(out.Thread[i].Links) == 0 && len(impPart.Links) > 0 {
				out.Thread[i].Links = append([]ItemLink(nil), impPart.Links...)
			}
			if len(out.Thread[i].MediaIndexes) == 0 && len(impPart.MediaIndexes) > 0 {
				out.Thread[i].MediaIndexes = append([]int(nil), impPart.MediaIndexes...)
			}
			out.Thread[i].Origin = mergeItemOriginDepth(out.Thread[i].Origin, impPart.Origin, 0)
		}
	}
	out.Origin = mergeItemOriginDepth(out.Origin, imported.Origin, 0)

	var renames []SegmentRename
	if imported.Live != nil {
		if out.Live == nil {
			lv := *imported.Live
			lv.Segments = append([]LiveSegmentInfo(nil), lv.Segments...)
			lv.Incidents = append([]LiveIncident(nil), lv.Incidents...)
			out.Live = &lv
		} else {
			lv := *out.Live
			lv.Segments = append([]LiveSegmentInfo(nil), lv.Segments...)
			lv.Incidents = append([]LiveIncident(nil), lv.Incidents...)
			byName := map[string]LiveSegmentInfo{}
			byContent := map[string]bool{}
			next := 0
			for _, seg := range lv.Segments {
				byName[strings.ToLower(seg.File)] = seg
				if seg.SHA256 != "" {
					byContent[strings.ToLower(seg.SHA256)+"|"+strconv.FormatInt(seg.Bytes, 10)] = true
				}
				next = max(next, livePartNumber(seg.File))
			}
			for _, m := range out.Media {
				next = max(next, livePartNumber(m.Path))
			}
			importedMediaByPath := map[string]MediaFile{}
			for _, m := range imported.Media {
				importedMediaByPath[strings.ToLower(cleanManifestRel(m.Path))] = m
			}

			renamed := map[string]string{}
			needSeq := false
			for _, seg := range imported.Live.Segments {
				origFile := seg.File
				keyName := strings.ToLower(origFile)
				contentKey := ""
				if seg.SHA256 != "" {
					contentKey = strings.ToLower(seg.SHA256) + "|" + strconv.FormatInt(seg.Bytes, 10)
				}
				cur, taken := byName[keyName]
				switch {
				case contentKey != "" && byContent[contentKey]:
					// Already present locally (under same or earlier merged name).
					continue
				case taken && (sameSegment(cur, seg) || (sameContent != nil && sameContent(origFile))):
					// Already present locally under the same name.
					continue
				case taken && mode == MergeKeepLocal:
					// The local file stays; the conflicting imported segment is not taken.
					continue
				case taken || needSeq:
					// Two installations numbered from 001: append sequentially after local max.
					needSeq = true
					ext := filepath.Ext(origFile)
					if m := livePartName.FindStringSubmatch(origFile); m != nil {
						ext = m[2]
					}
					next++
					to := fmt.Sprintf("part-%03d%s", next, ext)
					if !strings.EqualFold(origFile, to) {
						renamed[origFile] = to
						renames = append(renames, SegmentRename{From: origFile, To: to})
					}
					seg.File = to
				default:
					// Non-conflicting supplement or gap fill: keep original filename.
					next = max(next, livePartNumber(origFile))
				}

				lv.Segments = append(lv.Segments, seg)
				byName[strings.ToLower(seg.File)] = seg
				if contentKey != "" {
					byContent[contentKey] = true
				}
				if _, exists := have[strings.ToLower(cleanManifestRel(seg.File))]; !exists {
					if m, ok := importedMediaByPath[strings.ToLower(cleanManifestRel(origFile))]; ok {
						m.Path = seg.File
						have[strings.ToLower(cleanManifestRel(seg.File))] = len(out.Media)
						out.Media = append(out.Media, m)
					} else {
						have[strings.ToLower(cleanManifestRel(seg.File))] = len(out.Media)
						out.Media = append(out.Media, MediaFile{
							Role:   "live_segment",
							Path:   seg.File,
							Bytes:  seg.Bytes,
							SHA256: seg.SHA256,
						})
					}
				}
			}

			seen := map[string]bool{}
			for _, in := range lv.Incidents {
				seen[incidentKey(in)] = true
			}
			for _, in := range imported.Live.Incidents {
				if to, ok := renamed[in.Part]; ok {
					in.Part = to
				}
				if !seen[incidentKey(in)] && len(lv.Incidents) < MaxLiveIncidentsPerSession {
					lv.Incidents = append(lv.Incidents, in)
					seen[incidentKey(in)] = true
				}
			}
			if lv.EndedAt == "" || (imported.Live.EndedAt != "" && imported.Live.EndedAt > lv.EndedAt) {
				lv.EndedAt, lv.EndTitle, lv.StopReason = imported.Live.EndedAt, firstNonEmptyString(imported.Live.EndTitle, lv.EndTitle), firstNonEmptyString(imported.Live.StopReason, lv.StopReason)
			}
			if imported.Live.StartedAt != "" && (lv.StartedAt == "" || imported.Live.StartedAt < lv.StartedAt) {
				lv.StartedAt = imported.Live.StartedAt
			}
			if imported.Live.LastSequenceAt != "" && (lv.LastSequenceAt == "" || imported.Live.LastSequenceAt > lv.LastSequenceAt) {
				lv.LastSequenceAt = imported.Live.LastSequenceAt
			}
			lv.PendingFile, lv.PendingBytes, lv.PendingDuration = "", 0, 0
			out.Live = &lv
		}
	}
	out.PendingParts = nil
	sanitizeManifestConflictFields(&out)
	return out, renames
}

func sameSegment(a, b LiveSegmentInfo) bool {
	return a.SHA256 != "" && strings.EqualFold(a.SHA256, b.SHA256) && a.Bytes == b.Bytes
}

func incidentKey(in LiveIncident) string {
	return in.Kind + "|" + in.At + "|" + in.Part
}

func livePartNumber(name string) int {
	m := livePartName.FindStringSubmatch(filepath.Base(name))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// PortableManifest is item.json as it goes into an export folder or the
// cloud: the runtime fields of this installation (the item folder's
// absolute path, the index key, the computed size) are left out.
func PortableManifest(meta ItemMetadata) ([]byte, error) {
	meta = cloneItemMetadataSlices(meta)
	meta.InReplyTo = sanitizeItemReplyTarget(meta.InReplyTo, meta.Site)
	meta.Article = sanitizeItemArticle(meta.Article, meta.Site)
	if meta.Origin != nil {
		sanitizeItemOriginDepth(meta.Origin, meta.Site, 0)
	}
	for i := range meta.Thread {
		if meta.Thread[i].Origin != nil {
			sanitizeItemOriginDepth(meta.Thread[i].Origin, meta.Site, 0)
			if isEmptyItemOrigin(meta.Thread[i].Origin) {
				meta.Thread[i].Origin = nil
			}
		}
	}
	sanitizeManifestConflictFields(&meta)
	meta.ItemDir, meta.ItemKey, meta.RawID, meta.TotalBytes = "", "", "", 0
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// UnsafeManifestPaths lists the registered paths of meta that are absolute,
// contain forbidden characters (\, :, \x00), or contain '.'/'..' segments;
// such a manifest is never imported (OS-independent).
func UnsafeManifestPaths(meta ItemMetadata) []string {
	var bad []string
	visitManifestFiles(meta, func(f ManifestFile) {
		rel := strings.TrimSpace(f.Path)
		if rel == "" {
			return
		}
		if strings.ContainsAny(rel, "\\:\x00") || strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
			bad = append(bad, rel)
			return
		}
		for _, seg := range strings.Split(rel, "/") {
			if seg == "" || seg == "." || seg == ".." {
				bad = append(bad, rel)
				return
			}
		}
	})
	return bad
}

// KeepListedFiles drops from meta every registered file keep rejects (files
// an import could not verify) and records each in Missing, so the item is
// partial rather than missing files; a later import adds them back. It
// returns the dropped paths.
func KeepListedFiles(meta ItemMetadata, keep func(rel string) bool) (ItemMetadata, []string) {
	meta = cloneItemMetadataSlices(meta)
	var dropped []string
	ok := func(rel string) bool {
		rel = strings.TrimSpace(rel)
		if rel == "" || keep(cleanManifestRel(filepath.ToSlash(rel))) {
			return true
		}
		dropped = append(dropped, cleanManifestRel(filepath.ToSlash(rel)))
		return false
	}
	var media []MediaFile
	for _, m := range meta.Media {
		if !ok(m.Path) {
			continue
		}
		if !ok(m.Motion) {
			m.Motion = ""
		}
		media = append(media, m)
	}
	meta.Media = media
	var parts []VideoPartRecord
	for _, p := range meta.Parts {
		if !ok(p.File) {
			continue
		}
		if !ok(p.Danmaku) {
			p.Danmaku = ""
		}
		if !ok(p.Audio.SavedAs) {
			p.Audio.SavedAs = ""
		}
		var subs []string
		for _, s := range p.Subtitles {
			if ok(s) {
				subs = append(subs, s)
			}
		}
		p.Subtitles = subs
		parts = append(parts, p)
	}
	meta.Parts = parts
	if meta.DouyinVideo != nil && !ok(meta.DouyinVideo.File) {
		meta.DouyinVideo = nil
	}
	for i := range meta.NotePages {
		np := &meta.NotePages[i]
		if !ok(np.Image) {
			np.Image = ""
		}
		if !ok(np.NoWatermark) {
			np.NoWatermark = ""
		}
		if !ok(np.Cover) {
			np.Cover = ""
		}
		if !ok(np.Motion) {
			np.Motion = ""
		}
		if !ok(np.Video) {
			np.Video = ""
		}
		if np.VideoInfo != nil && !ok(np.VideoInfo.File) {
			np.VideoInfo = nil
		}
	}
	if meta.Music != nil {
		if !ok(meta.Music.File) {
			meta.Music.File = ""
		}
		if !ok(meta.Music.Cover) {
			meta.Music.Cover = ""
		}
	}
	if meta.Live != nil {
		lv := *meta.Live
		var segs []LiveSegmentInfo
		for _, s := range lv.Segments {
			if ok(s.File) {
				segs = append(segs, s)
			}
		}
		lv.Segments = segs
		meta.Live = &lv
	}
	if meta.Article != nil {
		if !ok(meta.Article.CoverRel) {
			meta.Article.CoverRel = ""
		}
		for i := range meta.Article.Blocks {
			if !ok(meta.Article.Blocks[i].MediaRel) {
				meta.Article.Blocks[i].MediaRel = ""
			}
		}
	}
	seen := map[string]bool{}
	for _, d := range dropped {
		if seen[d] {
			continue
		}
		seen[d] = true
		meta.Missing = append(meta.Missing, MissingRecord{Item: d, Reason: "导入时缺失或校验不符，下次导入补上"})
	}
	return meta, dropped
}
