package service

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
)

const (
	ConflictDiffStatusSame        = "same"
	ConflictDiffStatusChanged     = "changed"
	ConflictDiffStatusLocalOnly   = "local_only"
	ConflictDiffStatusRemoteOnly  = "remote_only"
	ConflictDiffStatusUpgradeable = "upgradeable"
)

func conflictMediaStatusText(status string) string {
	switch status {
	case ConflictDiffStatusSame:
		return "相同"
	case ConflictDiffStatusChanged:
		return "已变更"
	case ConflictDiffStatusLocalOnly:
		return "仅本地有"
	case ConflictDiffStatusRemoteOnly:
		return "源站新增"
	case ConflictDiffStatusUpgradeable:
		return "可升级"
	default:
		return "已变更"
	}
}

// DiffTextSideBySide computes highlighted side-by-side text diff segments.
// Concatenating localSegs reproduces localText verbatim; concatenating
// remoteSegs reproduces remoteText verbatim.
func DiffTextSideBySide(localText, remoteText string) (changed bool, localSegs, remoteSegs []ConflictTextSegment) {
	if localText == remoteText {
		if localText == "" {
			return false, nil, nil
		}
		seg := []ConflictTextSegment{{Text: localText, Kind: "same"}}
		return false, seg, append([]ConflictTextSegment(nil), seg...)
	}
	if localText == "" {
		return true, nil, []ConflictTextSegment{{Text: remoteText, Kind: "added"}}
	}
	if remoteText == "" {
		return true, []ConflictTextSegment{{Text: localText, Kind: "removed"}}, nil
	}

	lt := tokenizeDiffText(localText)
	rt := tokenizeDiffText(remoteText)
	if int64(len(lt))*int64(len(rt)) > 160000 {
		// Long text fallback: line-level LCS, keeping bounded time/memory.
		lt = splitLinesPreserveNewline(localText)
		rt = splitLinesPreserveNewline(remoteText)
		if int64(len(lt))*int64(len(rt)) > 160000 {
			return true,
				[]ConflictTextSegment{{Text: localText, Kind: "removed"}},
				[]ConflictTextSegment{{Text: remoteText, Kind: "added"}}
		}
	}

	m, n := len(lt), len(rt)
	dp := make([]int, (m+1)*(n+1))
	stride := n + 1
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if lt[i] == rt[j] {
				dp[i*stride+j] = dp[(i+1)*stride+(j+1)] + 1
			} else {
				a := dp[(i+1)*stride+j]
				b := dp[i*stride+(j+1)]
				if a >= b {
					dp[i*stride+j] = a
				} else {
					dp[i*stride+j] = b
				}
			}
		}
	}

	appendSeg := func(dst []ConflictTextSegment, text, kind string) []ConflictTextSegment {
		if text == "" {
			return dst
		}
		if len(dst) > 0 && dst[len(dst)-1].Kind == kind {
			dst[len(dst)-1].Text += text
			return dst
		}
		return append(dst, ConflictTextSegment{Text: text, Kind: kind})
	}

	i, j := 0, 0
	for i < m && j < n {
		if lt[i] == rt[j] {
			localSegs = appendSeg(localSegs, lt[i], "same")
			remoteSegs = appendSeg(remoteSegs, rt[j], "same")
			i++
			j++
		} else if dp[(i+1)*stride+j] >= dp[i*stride+(j+1)] {
			localSegs = appendSeg(localSegs, lt[i], "removed")
			i++
		} else {
			remoteSegs = appendSeg(remoteSegs, rt[j], "added")
			j++
		}
	}
	for ; i < m; i++ {
		localSegs = appendSeg(localSegs, lt[i], "removed")
	}
	for ; j < n; j++ {
		remoteSegs = appendSeg(remoteSegs, rt[j], "added")
	}
	return true, localSegs, remoteSegs
}

func tokenizeDiffText(s string) []string {
	var tokens []string
	runes := []rune(s)
	for i := 0; i < len(runes); {
		r := runes[i]
		if r == '\r' && i+1 < len(runes) && runes[i+1] == '\n' {
			tokens = append(tokens, "\r\n")
			i += 2
			continue
		}
		if r == '\n' || r == '\t' {
			tokens = append(tokens, string(r))
			i++
			continue
		}
		if isASCIIWordRune(r) {
			j := i + 1
			for j < len(runes) && isASCIIWordRune(runes[j]) {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
			continue
		}
		if r == ' ' {
			j := i + 1
			for j < len(runes) && runes[j] == ' ' {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
			continue
		}
		tokens = append(tokens, string(r))
		i++
	}
	return tokens
}

func isASCIIWordRune(r rune) bool {
	return r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-')
}

func splitLinesPreserveNewline(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// InspectCacheConflictDiff loads the local cached version (V1) and fetches the live
// remote state (V2) on demand to return highlighted text diffs and side-by-side
// image/video comparisons for the Tasks › 内容冲突 diff modal.
func (b *Backend) InspectCacheConflictDiff(ctx context.Context, id string) (CacheConflictDiffDTO, error) {
	if b == nil {
		return CacheConflictDiffDTO{}, nil
	}
	if err := b.requireDesktop(); err != nil {
		return CacheConflictDiffDTO{}, err
	}
	if b.conflicts == nil {
		return CacheConflictDiffDTO{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该内容冲突记录"}
	}
	cleanID := strings.TrimSpace(id)
	if cleanID == "" {
		return CacheConflictDiffDTO{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "冲突记录编号不能为空"}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var rec cacheConflictRecord
	found := false
	for _, r := range b.conflicts.snapshotSorted() {
		if r.ID == cleanID {
			rec = r
			found = true
			break
		}
	}
	if !found {
		return CacheConflictDiffDTO{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该内容冲突记录"}
	}

	var localMeta library.ItemMetadata
	var hasLocal bool
	if b.lib != nil {
		localMeta, hasLocal, _ = b.lib.GetItem(rec.ItemKey)
		if !hasLocal {
			localMeta, hasLocal, _ = b.lib.GetItemMetaOnly(rec.ItemKey)
		}
	}

	conflictDTO := CacheConflictDTO{
		ID:                rec.ID,
		ItemKey:           rec.ItemKey,
		Site:              rec.Site,
		Kind:              rec.Kind,
		TargetID:          rec.TargetID,
		AuthorUID:         rec.AuthorUID,
		AuthorName:        rec.AuthorName,
		Title:             firstNonEmpty(rec.Title, localMeta.Title, rec.TargetID),
		DetectedAt:        rec.DetectedAt,
		Origin:            rec.Origin,
		ReasonKind:        rec.ReasonKind,
		ReasonLabel:       ConflictReasonLabel(rec.ReasonKind),
		LocalSummary:      rec.LocalSummary,
		RemoteSummary:     rec.RemoteSummary,
		RemoteFingerprint: rec.RemoteFingerprint,
		State:             rec.State,
		LastError:         rec.LastError,
	}
	if conflictDTO.AuthorUID != "" {
		conflictDTO.AvatarKey = b.AccountAvatarPath(rec.Site, conflictDTO.AuthorUID)
	}

	out := CacheConflictDiffDTO{
		Conflict: conflictDTO,
		Local: ConflictDiffSideDTO{
			Heading: "本地已缓存（旧版 V1）",
			Summary: firstNonEmpty(rec.LocalSummary, "本机已缓存版本"),
		},
		Remote: ConflictDiffSideDTO{
			Heading: "源站当前（新版 V2）",
			Summary: firstNonEmpty(rec.RemoteSummary, "源站最新状态"),
		},
	}

	localTitle, localText := extractLocalDiffText(localMeta)
	out.Local.Title = localTitle
	out.Local.Text = localText
	localMedia := extractLocalDiffMedia(localMeta)

	siteID, sErr := normalizeExplicitSiteID(rec.Site)
	if sErr != nil {
		out.RemoteError = sanitizeConflictText(sErr.Error())
		out.MediaPairs = buildLocalOnlyMediaPairs(localMedia)
		populateTextDiffs(&out, false)
		return out, nil
	}
	if authErr := b.requireSiteLogin(siteID); authErr != nil {
		out.RemoteError = fmt.Sprintf("请先登录%s后查看源站实时对比", siteDisplayNameCN(siteID))
		out.MediaPairs = buildLocalOnlyMediaPairs(localMedia)
		populateTextDiffs(&out, false)
		return out, nil
	}

	var remoteMedia []ConflictMediaSideDTO
	var fetchErr error

	switch siteID {
	case site.Bilibili:
		if rec.Kind == "video" {
			remoteMedia, fetchErr = b.inspectBilibiliVideoDiff(ctx, rec, localMeta, &out)
		} else {
			remoteMedia, fetchErr = b.inspectBilibiliDynamicDiff(ctx, rec, &out)
		}
	case site.Douyin:
		remoteMedia, fetchErr = b.inspectDouyinDiff(ctx, rec, &out)
	case site.Weibo:
		remoteMedia, fetchErr = b.inspectWeiboDiff(ctx, rec, &out)
	case site.X:
		remoteMedia, fetchErr = b.inspectXDiff(ctx, rec, &out)
	default:
		fetchErr = &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}

	if fetchErr != nil {
		out.RemoteError = sanitizeConflictText(fetchErr.Error())
		if site.IsKind(fetchErr, site.ErrKindUnavailable) ||
			site.IsKind(fetchErr, site.ErrKindForbidden) ||
			site.IsKind(fetchErr, site.ErrKindBlocked) ||
			bilibili.IsKind(fetchErr, bilibili.ErrKindUnavailable) ||
			bilibili.IsKind(fetchErr, bilibili.ErrKindForbidden) {
			out.Remote.Unavailable = true
			out.Remote.Note = "源站内容当前不可用或已失效，已保护本地缓存"
		}
		out.MediaPairs = buildLocalOnlyMediaPairs(localMedia)
		populateTextDiffs(&out, false)
		return out, nil
	}

	out.MediaPairs = alignConflictMediaPairs(localMedia, remoteMedia)
	populateTextDiffs(&out, true)
	return out, nil
}

func populateTextDiffs(out *CacheConflictDiffDTO, hasRemote bool) {
	if !hasRemote || out.Remote.Unavailable {
		if out.Local.Title != "" {
			out.LocalTitle = []ConflictTextSegment{{Text: out.Local.Title, Kind: "same"}}
		}
		if out.Local.Text != "" {
			out.LocalText = []ConflictTextSegment{{Text: out.Local.Text, Kind: "same"}}
		}
		return
	}
	if strings.TrimSpace(out.Local.Title) != "" || strings.TrimSpace(out.Remote.Title) != "" {
		if normComparableText(out.Local.Title) == normComparableText(out.Remote.Title) {
			out.TitleChanged = false
			if out.Local.Title != "" {
				out.LocalTitle = []ConflictTextSegment{{Text: out.Local.Title, Kind: "same"}}
			}
			if out.Remote.Title != "" {
				out.RemoteTitle = []ConflictTextSegment{{Text: out.Remote.Title, Kind: "same"}}
			}
		} else {
			out.TitleChanged, out.LocalTitle, out.RemoteTitle = DiffTextSideBySide(out.Local.Title, out.Remote.Title)
		}
	}
	if strings.TrimSpace(out.Local.Text) != "" || strings.TrimSpace(out.Remote.Text) != "" {
		if normComparableText(out.Local.Text) == normComparableText(out.Remote.Text) {
			out.TextChanged = false
			if out.Local.Text != "" {
				out.LocalText = []ConflictTextSegment{{Text: out.Local.Text, Kind: "same"}}
			}
			if out.Remote.Text != "" {
				out.RemoteText = []ConflictTextSegment{{Text: out.Remote.Text, Kind: "same"}}
			}
		} else {
			out.TextChanged, out.LocalText, out.RemoteText = DiffTextSideBySide(out.Local.Text, out.Remote.Text)
		}
	}
}

func extractLocalDiffText(meta library.ItemMetadata) (title, body string) {
	siteID := strings.ToLower(strings.TrimSpace(meta.Site))
	kind := strings.ToLower(strings.TrimSpace(meta.Kind))
	title = strings.TrimSpace(meta.Title)
	// Weibo non-video posts and X posts do not carry a real upstream title;
	// their cached meta.Title is a synthetic summary truncated from body text.
	if siteID == "x" || (siteID == "weibo" && kind != "video") {
		title = ""
	}
	if kind == "video" && siteID == "bilibili" {
		body = strings.TrimSpace(meta.Desc)
		return title, body
	}
	if len(meta.Thread) > 0 {
		var parts []string
		for _, th := range meta.Thread {
			if t := strings.TrimSpace(th.Text); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			return title, strings.Join(parts, "\n\n")
		}
	}
	body = strings.TrimSpace(firstNonEmpty(meta.Text, meta.Desc))
	return title, body
}

func extractLocalDiffMedia(meta library.ItemMetadata) []ConflictMediaSideDTO {
	if strings.TrimSpace(meta.ItemKey) == "" {
		return nil
	}
	coverRel := findLocalCoverRel(meta)
	var out []ConflictMediaSideDTO

	// 1. Bilibili multi-part video
	if len(meta.Parts) > 0 {
		for idx, p := range meta.Parts {
			pageNo := p.Page
			if pageNo <= 0 {
				pageNo = idx + 1
			}
			label := fmt.Sprintf("P%02d", pageNo)
			if strings.TrimSpace(p.Title) != "" {
				label += " " + strings.TrimSpace(p.Title)
			}
			var specs []string
			if strings.TrimSpace(p.Video.QualityName) != "" {
				specs = append(specs, strings.TrimSpace(p.Video.QualityName))
			}
			if strings.TrimSpace(p.Video.Codecs) != "" {
				specs = append(specs, strings.TrimSpace(p.Video.Codecs))
			}
			if p.Duration > 0 {
				specs = append(specs, formatDiffDuration(p.Duration))
			}
			if p.CID > 0 {
				specs = append(specs, fmt.Sprintf("CID %d", p.CID))
			}
			out = append(out, ConflictMediaSideDTO{
				Kind:         "video",
				Label:        label,
				Sublabel:     strings.Join(specs, " · "),
				LocalItemKey: meta.ItemKey,
				LocalRelPath: coverRel,
				PlayRelPath:  p.File,
				DurationSec:  p.Duration,
				CID:          p.CID,
			})
		}
		return out
	}

	// 2. Multi-page notes / mixed posts + single video (Douyin / Weibo / X).
	// Some retweets/mixed posts hold both NotePages (outer images) and DouyinVideo (origin video),
	// so extract both without returning early before checking the other.
	if len(meta.NotePages) > 0 {
		for idx, pg := range meta.NotePages {
			n := pg.Index
			if n <= 0 {
				n = idx + 1
			}
			kind := "image"
			label := fmt.Sprintf("图 %d", n)
			rel := pg.Image
			playRel := ""
			if pg.Kind == "video" && !pg.GIF {
				kind = "video"
				label = fmt.Sprintf("视频 %d", n)
				rel = firstNonEmpty(pg.Cover, coverRel, pg.Image)
				playRel = pg.Video
			} else if pg.GIF {
				label = fmt.Sprintf("动图 %d", n)
				rel = firstNonEmpty(pg.Image, pg.Cover, coverRel)
			} else if pg.Kind == "live_photo" || strings.TrimSpace(pg.Motion) != "" {
				label = fmt.Sprintf("实况 %d", n)
			}
			var specs []string
			if pg.Width > 0 && pg.Height > 0 {
				specs = append(specs, fmt.Sprintf("%d×%d", pg.Width, pg.Height))
			}
			if pg.Duration > 0 {
				specs = append(specs, formatDiffDuration(pg.Duration))
			}
			if pg.VideoInfo != nil && strings.TrimSpace(pg.VideoInfo.QualityName) != "" {
				specs = append(specs, pg.VideoInfo.QualityName)
			}
			if pg.Kind == "live_photo" || strings.TrimSpace(pg.Motion) != "" {
				specs = append(specs, "实况")
			}
			if pg.GIF {
				specs = append(specs, "GIF")
			}
			out = append(out, ConflictMediaSideDTO{
				Kind:         kind,
				Label:        label,
				Sublabel:     strings.Join(specs, " · "),
				LocalItemKey: meta.ItemKey,
				LocalRelPath: rel,
				PlayRelPath:  playRel,
				DurationSec:  pg.Duration,
				Width:        pg.Width,
				Height:       pg.Height,
				MediaID:      pg.MediaID,
				LivePhoto:    pg.Kind == "live_photo" || strings.TrimSpace(pg.Motion) != "",
				GIF:          pg.GIF,
			})
		}
	}
	if meta.DouyinVideo != nil && strings.TrimSpace(meta.DouyinVideo.File) != "" {
		v := meta.DouyinVideo
		var specs []string
		if strings.TrimSpace(v.QualityName) != "" {
			specs = append(specs, strings.TrimSpace(v.QualityName))
		}
		if strings.TrimSpace(v.Codec) != "" {
			specs = append(specs, strings.TrimSpace(v.Codec))
		}
		if v.Width > 0 && v.Height > 0 {
			specs = append(specs, fmt.Sprintf("%d×%d", v.Width, v.Height))
		}
		if meta.Duration > 0 {
			specs = append(specs, formatDiffDuration(meta.Duration))
		}
		out = append(out, ConflictMediaSideDTO{
			Kind:         "video",
			Label:        "主视频",
			Sublabel:     strings.Join(specs, " · "),
			LocalItemKey: meta.ItemKey,
			LocalRelPath: coverRel,
			PlayRelPath:  v.File,
			DurationSec:  meta.Duration,
			Width:        v.Width,
			Height:       v.Height,
			MediaID:      v.MediaID,
		})
	}
	if len(out) > 0 {
		return out
	}

	// 4. Generic Media files (Bilibili dynamics, etc.)
	imgIdx, vidIdx := 0, 0
	for _, mf := range meta.Media {
		switch mf.Role {
		case "image":
			imgIdx++
			label := fmt.Sprintf("图 %d", imgIdx)
			var specs []string
			if mf.Width > 0 && mf.Height > 0 {
				specs = append(specs, fmt.Sprintf("%d×%d", mf.Width, mf.Height))
			}
			if strings.TrimSpace(mf.Motion) != "" {
				label = fmt.Sprintf("实况 %d", imgIdx)
				specs = append(specs, "实况")
			}
			if mf.GIF {
				specs = append(specs, "GIF")
			}
			out = append(out, ConflictMediaSideDTO{
				Kind:         "image",
				Label:        label,
				Sublabel:     strings.Join(specs, " · "),
				LocalItemKey: meta.ItemKey,
				LocalRelPath: mf.Path,
				Width:        mf.Width,
				Height:       mf.Height,
				MediaID:      diffURLPathToken(mf.Source),
				LivePhoto:    strings.TrimSpace(mf.Motion) != "",
				GIF:          mf.GIF,
			})
		case "video":
			vidIdx++
			label := fmt.Sprintf("视频 %d", vidIdx)
			var specs []string
			if mf.Width > 0 && mf.Height > 0 {
				specs = append(specs, fmt.Sprintf("%d×%d", mf.Width, mf.Height))
			}
			if mf.Duration > 0 {
				specs = append(specs, formatDiffDuration(mf.Duration))
			}
			out = append(out, ConflictMediaSideDTO{
				Kind:         "video",
				Label:        label,
				Sublabel:     strings.Join(specs, " · "),
				LocalItemKey: meta.ItemKey,
				LocalRelPath: coverRel,
				PlayRelPath:  mf.Path,
				DurationSec:  mf.Duration,
				Width:        mf.Width,
				Height:       mf.Height,
			})
		}
	}
	// Bilibili dynamics store linked video references ("video:BV...") in meta.Videos.
	for _, ref := range meta.Videos {
		bvid := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), "video:"))
		if bvid == "" {
			continue
		}
		vidIdx++
		out = append(out, ConflictMediaSideDTO{
			Kind:         "video",
			Label:        fmt.Sprintf("关联视频 %d", vidIdx),
			Sublabel:     bvid,
			LocalItemKey: meta.ItemKey,
			LocalRelPath: coverRel,
			MediaID:      bvid,
		})
	}
	return out
}

func findLocalCoverRel(meta library.ItemMetadata) string {
	for _, mf := range meta.Media {
		if mf.Role == "cover" && strings.TrimSpace(mf.Path) != "" {
			return mf.Path
		}
	}
	for _, mf := range meta.Media {
		if mf.Role == "image" && strings.TrimSpace(mf.Path) != "" {
			return mf.Path
		}
	}
	return ""
}

func (b *Backend) inspectBilibiliVideoDiff(ctx context.Context, rec cacheConflictRecord, localMeta library.ItemMetadata, out *CacheConflictDiffDTO) ([]ConflictMediaSideDTO, error) {
	if b.bili == nil {
		return nil, &site.Error{Site: site.Bilibili, Kind: site.ErrKindUnavailable, Message: "哔哩哔哩客户端不可用"}
	}
	vd, err := b.bili.VideoDetail(ctx, rec.TargetID)
	if err != nil {
		return nil, err
	}
	out.Remote.Title = strings.TrimSpace(vd.Title)
	out.Remote.Text = strings.TrimSpace(vd.Desc)
	if len(vd.Pages) == 0 {
		out.Remote.Unavailable = true
		out.Remote.Note = "源站视频已无可用分P"
		return nil, nil
	}
	coverURL := normalizePublicPreviewURL(vd.Cover)
	media := make([]ConflictMediaSideDTO, 0, len(vd.Pages))
	for idx, p := range vd.Pages {
		pageNo := p.Page
		if pageNo <= 0 {
			pageNo = idx + 1
		}
		label := fmt.Sprintf("P%02d", pageNo)
		if strings.TrimSpace(p.Title) != "" {
			label += " " + strings.TrimSpace(p.Title)
		}
		var specs []string
		if p.Width > 0 && p.Height > 0 {
			specs = append(specs, fmt.Sprintf("%d×%d", p.Width, p.Height))
		}
		if p.Duration > 0 {
			specs = append(specs, formatDiffDuration(p.Duration))
		}
		if p.CID > 0 {
			specs = append(specs, fmt.Sprintf("CID %d", p.CID))
		}
		media = append(media, ConflictMediaSideDTO{
			Kind:        "video",
			Label:       label,
			Sublabel:    strings.Join(specs, " · "),
			RemoteURL:   coverURL,
			DurationSec: p.Duration,
			Width:       p.Width,
			Height:      p.Height,
			CID:         p.CID,
		})
	}
	if len(vd.Pages) > 0 && localMeta.Title != "" && out.Remote.Summary == "" {
		out.Remote.Summary = fmt.Sprintf("共 %d P", len(vd.Pages))
	}
	return media, nil
}

func (b *Backend) inspectBilibiliDynamicDiff(ctx context.Context, rec cacheConflictRecord, out *CacheConflictDiffDTO) ([]ConflictMediaSideDTO, error) {
	if b.bili == nil {
		return nil, &site.Error{Site: site.Bilibili, Kind: site.ErrKindUnavailable, Message: "哔哩哔哩客户端不可用"}
	}
	dyn, err := b.bili.DynamicDetail(ctx, rec.TargetID)
	if err != nil {
		return nil, err
	}
	out.Remote.Title = strings.TrimSpace(dyn.Title)
	out.Remote.Text = strings.TrimSpace(dyn.Text)
	if dyn.Degraded {
		out.Remote.Unavailable = true
		out.Remote.Note = "源站动态当前不可用或已失效"
		return nil, nil
	}
	imgs, vids := effectiveBiliDynamicMedia(dyn)
	media := make([]ConflictMediaSideDTO, 0, len(imgs)+len(vids))
	for i, img := range imgs {
		label := fmt.Sprintf("图 %d", i+1)
		var specs []string
		if img.Width > 0 && img.Height > 0 {
			specs = append(specs, fmt.Sprintf("%d×%d", img.Width, img.Height))
		}
		if strings.TrimSpace(img.LiveURL) != "" {
			label = fmt.Sprintf("实况 %d", i+1)
			specs = append(specs, "实况")
		}
		media = append(media, ConflictMediaSideDTO{
			Kind:      "image",
			Label:     label,
			Sublabel:  strings.Join(specs, " · "),
			RemoteURL: normalizePublicPreviewURL(img.URL),
			Width:     img.Width,
			Height:    img.Height,
			MediaID:   diffURLPathToken(img.URL),
			LivePhoto: strings.TrimSpace(img.LiveURL) != "",
		})
	}
	for i, v := range vids {
		label := firstNonEmpty(strings.TrimSpace(v.Title), fmt.Sprintf("视频 %d", i+1))
		var specs []string
		if v.DurationSec > 0 {
			specs = append(specs, formatDiffDuration(v.DurationSec))
		}
		if strings.TrimSpace(v.BVID) != "" {
			specs = append(specs, v.BVID)
		}
		media = append(media, ConflictMediaSideDTO{
			Kind:        "video",
			Label:       label,
			Sublabel:    strings.Join(specs, " · "),
			RemoteURL:   normalizePublicPreviewURL(v.Cover),
			DurationSec: v.DurationSec,
			MediaID:     strings.TrimSpace(v.BVID),
		})
	}
	return media, nil
}

func (b *Backend) inspectDouyinDiff(ctx context.Context, rec cacheConflictRecord, out *CacheConflictDiffDTO) ([]ConflictMediaSideDTO, error) {
	if b.douyin == nil {
		return nil, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "抖音客户端不可用"}
	}
	detail, err := b.douyin.GetWorkDetail(ctx, rec.TargetID, douyin.StreamSelectOptions{
		PreferredCodec: b.preferredDouyinCodec(),
	})
	if err != nil {
		return nil, err
	}
	if detail == nil || detail.Uncacheable || detail.Kind == douyin.WorkKindUnsupported {
		out.Remote.Unavailable = true
		if detail != nil && strings.TrimSpace(detail.UncacheableReason) != "" {
			out.Remote.Note = detail.UncacheableReason
		} else {
			out.Remote.Note = "源站作品当前受限或已删除"
		}
		return nil, nil
	}
	out.Remote.Title = strings.TrimSpace(detail.Title)
	out.Remote.Text = strings.TrimSpace(detail.Desc)

	return extractDouyinRemoteDiffMedia(detail), nil
}

func extractDouyinRemoteDiffMedia(detail *douyin.WorkDetail) []ConflictMediaSideDTO {
	if detail == nil {
		return nil
	}
	coverURL := ""
	if len(detail.CoverURLs) > 0 {
		coverURL = detail.CoverURLs[0]
	}

	if detail.Kind == douyin.WorkKindNote || len(detail.Pages) > 0 {
		media := make([]ConflictMediaSideDTO, 0, len(detail.Pages))
		for idx, pg := range detail.Pages {
			n := pg.Index
			if n <= 0 {
				n = idx + 1
			}
			kind := "image"
			label := fmt.Sprintf("图 %d", n)
			previewURL := ""
			if len(pg.ImageURLs) > 0 {
				previewURL = pg.ImageURLs[0]
			}
			var specs []string
			if pg.Kind == douyin.NotePageVideo {
				kind = "video"
				label = fmt.Sprintf("视频 %d", n)
				if previewURL == "" {
					previewURL = coverURL
				}
			} else if pg.Kind == douyin.NotePageLivePhoto {
				label = fmt.Sprintf("实况 %d", n)
				specs = append(specs, "实况")
			}
			if pg.Width > 0 && pg.Height > 0 {
				specs = append(specs, fmt.Sprintf("%d×%d", pg.Width, pg.Height))
			}
			if pg.VideoStream != nil && strings.TrimSpace(pg.VideoStream.QualityLabel) != "" {
				specs = append(specs, pg.VideoStream.QualityLabel)
			}
			media = append(media, ConflictMediaSideDTO{
				Kind:      kind,
				Label:     label,
				Sublabel:  strings.Join(specs, " · "),
				RemoteURL: previewURL,
				Width:     pg.Width,
				Height:    pg.Height,
				MediaID:   douyinPageMediaID(pg),
				LivePhoto: pg.Kind == douyin.NotePageLivePhoto,
			})
		}
		return media
	}

	var specs []string
	var width, height int
	if detail.Video != nil {
		if strings.TrimSpace(detail.Video.QualityLabel) != "" {
			specs = append(specs, detail.Video.QualityLabel)
		}
		if strings.TrimSpace(detail.Video.Codec) != "" {
			specs = append(specs, detail.Video.Codec)
		}
		width, height = detail.Video.Width, detail.Video.Height
		if width > 0 && height > 0 {
			specs = append(specs, fmt.Sprintf("%d×%d", width, height))
		}
	}
	if detail.DurationSec > 0 {
		specs = append(specs, formatDiffDuration(detail.DurationSec))
	}
	return []ConflictMediaSideDTO{{
		Kind:        "video",
		Label:       "主视频",
		Sublabel:    strings.Join(specs, " · "),
		RemoteURL:   coverURL,
		DurationSec: detail.DurationSec,
		Width:       width,
		Height:      height,
		MediaID:     douyinDetailMediaID(detail),
	}}
}

func (b *Backend) inspectWeiboDiff(ctx context.Context, rec cacheConflictRecord, out *CacheConflictDiffDTO) ([]ConflictMediaSideDTO, error) {
	if b.weibo == nil {
		return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindUnavailable, Message: "微博客户端不可用"}
	}
	postOpt := weibo.PostOptions{
		StreamOptions: weibo.StreamSelectOptions{
			PreferredCodec: b.preferredWeiboCodec(),
			MaxResolution:  b.currentWeiboMaxResolution(),
		},
	}
	if rec.Kind == "video" {
		oid := rec.TargetID
		if strings.HasPrefix(oid, "1034_") {
			oid = "1034:" + strings.TrimPrefix(oid, "1034_")
		}
		vr, err := b.weibo.GetVideo(ctx, oid, postOpt)
		if err != nil {
			return nil, err
		}
		if vr == nil || (vr.HostPost == nil && vr.Video == nil) {
			out.Remote.Unavailable = true
			out.Remote.Note = "源站微博视频已失效"
			return nil, nil
		}
		if vr.HostPost != nil {
			out.Remote.Text = strings.TrimSpace(vr.HostPost.Text)
		}
		if vr.Video != nil {
			out.Remote.Title = strings.TrimSpace(vr.Video.Title)
			var specs []string
			var width, height int
			if vr.Video.Selected != nil {
				if strings.TrimSpace(vr.Video.Selected.QualityLabel) != "" {
					specs = append(specs, vr.Video.Selected.QualityLabel)
				}
				width, height = vr.Video.Selected.Width, vr.Video.Selected.Height
				if width > 0 && height > 0 {
					specs = append(specs, fmt.Sprintf("%d×%d", width, height))
				}
			}
			if vr.Video.DurationSec > 0 {
				specs = append(specs, formatDiffDuration(vr.Video.DurationSec))
			}
			return []ConflictMediaSideDTO{{
				Kind:        "video",
				Label:       "主视频",
				Sublabel:    strings.Join(specs, " · "),
				RemoteURL:   vr.Video.CoverURL,
				DurationSec: vr.Video.DurationSec,
				Width:       width,
				Height:      height,
				MediaID:     library.SanitizeMediaToken(vr.Video.ObjectID),
			}}, nil
		}
		return nil, nil
	}

	post, err := b.weibo.GetPost(ctx, rec.TargetID, postOpt)
	if err != nil {
		return nil, err
	}
	if post == nil || post.Unavailable {
		out.Remote.Unavailable = true
		if post != nil && strings.TrimSpace(post.UnavailableReason) != "" {
			out.Remote.Note = post.UnavailableReason
		} else {
			out.Remote.Note = "源站微博已不可见或已删除"
		}
		return nil, nil
	}
	out.Remote.Text = strings.TrimSpace(post.Text)
	return extractWeiboPostRemoteDiffMedia(post), nil
}

func extractWeiboPostRemoteDiffMedia(post *weibo.Post) []ConflictMediaSideDTO {
	if post == nil {
		return nil
	}
	imgs, vid := post.EffectiveMedia()
	media := make([]ConflictMediaSideDTO, 0, len(imgs)+1)
	for i, img := range imgs {
		kind := "image"
		label := fmt.Sprintf("图 %d", i+1)
		var specs []string
		switch img.Kind {
		case weibo.MediaKindVideo:
			kind = "video"
			label = fmt.Sprintf("视频 %d", i+1)
		case weibo.MediaKindLivePhoto:
			label = fmt.Sprintf("实况 %d", i+1)
			specs = append(specs, "实况")
		case weibo.MediaKindGIF:
			label = fmt.Sprintf("动图 %d", i+1)
			specs = append(specs, "GIF")
		}
		if img.Width > 0 && img.Height > 0 {
			specs = append(specs, fmt.Sprintf("%d×%d", img.Width, img.Height))
		}
		if img.DurationSec > 0 {
			specs = append(specs, formatDiffDuration(img.DurationSec))
		}
		previewURL := firstNonEmpty(img.PublicPreviewURL, img.URL)
		media = append(media, ConflictMediaSideDTO{
			Kind:        kind,
			Label:       label,
			Sublabel:    strings.Join(specs, " · "),
			RemoteURL:   previewURL,
			DurationSec: img.DurationSec,
			Width:       img.Width,
			Height:      img.Height,
			MediaID:     weiboImageMediaID(img),
			LivePhoto:   img.Kind == weibo.MediaKindLivePhoto,
			GIF:         img.Kind == weibo.MediaKindGIF,
		})
	}
	if vid != nil {
		var specs []string
		var width, height int
		if vid.Selected != nil {
			if strings.TrimSpace(vid.Selected.QualityLabel) != "" {
				specs = append(specs, vid.Selected.QualityLabel)
			}
			width, height = vid.Selected.Width, vid.Selected.Height
			if width > 0 && height > 0 {
				specs = append(specs, fmt.Sprintf("%d×%d", width, height))
			}
		}
		if vid.DurationSec > 0 {
			specs = append(specs, formatDiffDuration(vid.DurationSec))
		}
		media = append(media, ConflictMediaSideDTO{
			Kind:        "video",
			Label:       "主视频",
			Sublabel:    strings.Join(specs, " · "),
			RemoteURL:   vid.CoverURL,
			DurationSec: vid.DurationSec,
			Width:       width,
			Height:      height,
			MediaID:     library.SanitizeMediaToken(vid.ObjectID),
		})
	}
	return media
}

func (b *Backend) inspectXDiff(ctx context.Context, rec cacheConflictRecord, out *CacheConflictDiffDTO) ([]ConflictMediaSideDTO, error) {
	if b.x == nil {
		return nil, &site.Error{Site: site.X, Kind: site.ErrKindUnavailable, Message: "X 客户端不可用"}
	}
	post, err := b.x.GetPost(ctx, rec.TargetID)
	if err != nil {
		return nil, err
	}
	if post == nil || post.Unavailable || post.IsPreviewDisplay {
		out.Remote.Unavailable = true
		if post != nil && strings.TrimSpace(post.UnavailableReason) != "" {
			out.Remote.Note = post.UnavailableReason
		} else {
			out.Remote.Note = "源站帖子已不可用或受限"
		}
		return nil, nil
	}
	text, media := extractXRemoteDiffTextAndMedia(post)
	out.Remote.Text = text
	return media, nil
}

func extractXRemoteDiffTextAndMedia(post *x.Post) (string, []ConflictMediaSideDTO) {
	if post == nil {
		return "", nil
	}
	var textParts []string
	seenIDs := make(map[string]bool, len(post.ThreadAncestors)+1+len(post.Thread))
	appendPostText := func(id, text string) {
		trimmedID := strings.TrimSpace(id)
		if trimmedID != "" {
			if seenIDs[trimmedID] {
				return
			}
			seenIDs[trimmedID] = true
		}
		if t := strings.TrimSpace(text); t != "" {
			textParts = append(textParts, t)
		}
	}
	for i := range post.ThreadAncestors {
		appendPostText(post.ThreadAncestors[i].ID, post.ThreadAncestors[i].Text)
	}
	appendPostText(post.ID, post.Text)
	for i := range post.Thread {
		appendPostText(post.Thread[i].ID, post.Thread[i].Text)
	}

	mediaItems, singleVideo, _ := post.EffectiveMediaWithOrigin()
	hasThreadExtraMedia := false
	for i := range post.ThreadAncestors {
		if len(post.ThreadAncestors[i].Media) > 0 {
			hasThreadExtraMedia = true
			break
		}
	}
	if !hasThreadExtraMedia {
		for i := range post.Thread {
			if len(post.Thread[i].Media) > 0 {
				hasThreadExtraMedia = true
				break
			}
		}
	}

	if singleVideo != nil && !hasThreadExtraMedia {
		return strings.Join(textParts, "\n\n"), []ConflictMediaSideDTO{xMediaItemToConflictSide(*singleVideo, 1, true)}
	}

	var combined []x.MediaItem
	for i := range post.ThreadAncestors {
		combined = append(combined, post.ThreadAncestors[i].Media...)
	}
	if singleVideo != nil {
		combined = append(combined, *singleVideo)
	} else {
		combined = append(combined, mediaItems...)
	}
	for i := range post.Thread {
		combined = append(combined, post.Thread[i].Media...)
	}

	media := make([]ConflictMediaSideDTO, 0, len(combined))
	for i, m := range combined {
		media = append(media, xMediaItemToConflictSide(m, i+1, false))
	}
	return strings.Join(textParts, "\n\n"), media
}

func xMediaItemToConflictSide(m x.MediaItem, index int, isSingleVideo bool) ConflictMediaSideDTO {
	kind := "image"
	label := fmt.Sprintf("图 %d", index)
	var specs []string
	if isSingleVideo {
		kind = "video"
		label = "主视频"
	} else if m.Kind == x.MediaKindVideo && !m.GIF {
		kind = "video"
		label = fmt.Sprintf("视频 %d", index)
	} else if m.GIF || m.Kind == x.MediaKindGIF {
		label = fmt.Sprintf("动图 %d", index)
		specs = append(specs, "GIF")
	}
	if m.Width > 0 && m.Height > 0 {
		specs = append(specs, fmt.Sprintf("%d×%d", m.Width, m.Height))
	}
	if m.DurationSec > 0 {
		specs = append(specs, formatDiffDuration(m.DurationSec))
	}
	if m.HLSOnly {
		specs = append(specs, "仅分段流")
	}
	previewURL := firstNonEmpty(m.PublicPreviewURL, m.ImageURL)
	return ConflictMediaSideDTO{
		Kind:        kind,
		Label:       label,
		Sublabel:    strings.Join(specs, " · "),
		RemoteURL:   previewURL,
		DurationSec: m.DurationSec,
		Width:       m.Width,
		Height:      m.Height,
		MediaID:     library.SanitizeMediaToken(m.MediaKey),
		GIF:         m.GIF || m.Kind == x.MediaKindGIF,
	}
}

func alignConflictMediaPairs(local, remote []ConflictMediaSideDTO) []ConflictMediaPairDTO {
	if len(local) == 0 && len(remote) == 0 {
		return nil
	}
	usedRemote := make([]bool, len(remote))
	pairs := make([]ConflictMediaPairDTO, 0, max(len(local), len(remote)))

	// Match local slots against remote by stable identity first (CID or MediaID),
	// then fall back to positional slot alignment so replacements appear side-by-side.
	for i := range local {
		loc := local[i]
		matchedIdx := -1
		if loc.CID > 0 {
			for j := range remote {
				if !usedRemote[j] && remote[j].CID == loc.CID {
					matchedIdx = j
					break
				}
			}
		}
		if matchedIdx < 0 && loc.MediaID != "" {
			for j := range remote {
				if !usedRemote[j] && remote[j].MediaID == loc.MediaID && remote[j].Kind == loc.Kind {
					matchedIdx = j
					break
				}
			}
		}
		if matchedIdx < 0 && i < len(remote) && !usedRemote[i] {
			// Only align positionally if remote[i] doesn't match a different local item by CID/MediaID.
			belongsElsewhere := false
			if remote[i].CID > 0 {
				for k := i + 1; k < len(local); k++ {
					if local[k].CID == remote[i].CID {
						belongsElsewhere = true
						break
					}
				}
			} else if remote[i].MediaID != "" {
				for k := i + 1; k < len(local); k++ {
					if local[k].MediaID == remote[i].MediaID {
						belongsElsewhere = true
						break
					}
				}
			}
			if !belongsElsewhere {
				matchedIdx = i
			}
		}

		locCopy := loc
		if matchedIdx < 0 {
			pairs = append(pairs, ConflictMediaPairDTO{
				Index:      len(pairs) + 1,
				Kind:       loc.Kind,
				Status:     ConflictDiffStatusLocalOnly,
				StatusText: conflictMediaStatusText(ConflictDiffStatusLocalOnly),
				Local:      &locCopy,
			})
			continue
		}
		usedRemote[matchedIdx] = true
		remCopy := remote[matchedIdx]
		status := classifyMediaPairStatus(locCopy, remCopy)
		pairs = append(pairs, ConflictMediaPairDTO{
			Index:      len(pairs) + 1,
			Kind:       firstNonEmpty(locCopy.Kind, remCopy.Kind),
			Status:     status,
			StatusText: conflictMediaStatusText(status),
			Local:      &locCopy,
			Remote:     &remCopy,
		})
	}

	for j := range remote {
		if usedRemote[j] {
			continue
		}
		remCopy := remote[j]
		pairs = append(pairs, ConflictMediaPairDTO{
			Index:      len(pairs) + 1,
			Kind:       remCopy.Kind,
			Status:     ConflictDiffStatusRemoteOnly,
			StatusText: conflictMediaStatusText(ConflictDiffStatusRemoteOnly),
			Remote:     &remCopy,
		})
	}
	return pairs
}

func classifyMediaPairStatus(loc, rem ConflictMediaSideDTO) string {
	if loc.Kind != rem.Kind {
		return ConflictDiffStatusChanged
	}
	if loc.CID > 0 && rem.CID > 0 && loc.CID != rem.CID {
		return ConflictDiffStatusChanged
	}
	if loc.MediaID != "" && rem.MediaID != "" && loc.MediaID != rem.MediaID {
		return ConflictDiffStatusChanged
	}
	if loc.DurationSec > 0 && rem.DurationSec > 0 {
		diff := loc.DurationSec - rem.DurationSec
		if diff < 0 {
			diff = -diff
		}
		tol := max(2, int(float64(max(loc.DurationSec, rem.DurationSec))*0.03))
		if diff > tol {
			return ConflictDiffStatusChanged
		}
	}
	if loc.Width > 0 && rem.Width > 0 && loc.Height > 0 && rem.Height > 0 {
		if rem.Width >= loc.Width && rem.Height >= loc.Height && (rem.Width > loc.Width || rem.Height > loc.Height) && (loc.CID == rem.CID) && (loc.MediaID == "" || loc.MediaID == rem.MediaID) {
			return ConflictDiffStatusUpgradeable
		}
		if loc.Width != rem.Width || loc.Height != rem.Height {
			return ConflictDiffStatusChanged
		}
	}
	if loc.LivePhoto != rem.LivePhoto || loc.GIF != rem.GIF {
		return ConflictDiffStatusChanged
	}
	return ConflictDiffStatusSame
}

func buildLocalOnlyMediaPairs(local []ConflictMediaSideDTO) []ConflictMediaPairDTO {
	if len(local) == 0 {
		return nil
	}
	pairs := make([]ConflictMediaPairDTO, 0, len(local))
	for i := range local {
		locCopy := local[i]
		pairs = append(pairs, ConflictMediaPairDTO{
			Index:      i + 1,
			Kind:       locCopy.Kind,
			Status:     ConflictDiffStatusLocalOnly,
			StatusText: "仅本地预览",
			Local:      &locCopy,
		})
	}
	return pairs
}

func normalizePublicPreviewURL(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "//") {
		s = "https:" + s
	}
	return s
}

func diffURLPathToken(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if idx := strings.IndexAny(s, "?#@"); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.LastIndex(s, "/"); idx >= 0 && idx+1 < len(s) {
		s = s[idx+1:]
	}
	return library.SanitizeMediaToken(s)
}

func formatDiffDuration(sec int) string {
	if sec <= 0 {
		return ""
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func siteDisplayNameCN(id site.ID) string {
	switch id {
	case site.Bilibili:
		return "哔哩哔哩"
	case site.Douyin:
		return "抖音"
	case site.Weibo:
		return "微博"
	case site.X:
		return "X"
	default:
		return string(id)
	}
}
