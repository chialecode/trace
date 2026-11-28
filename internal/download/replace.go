package download

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

// RecoverInterruptedItemSwap delegates to library.RecoverInterruptedItemSwap so
// callers in either package can recover interrupted ".trace-prev-*" swaps.
func RecoverInterruptedItemSwap(itemDir string, meta library.ItemMetadata) error {
	return library.RecoverInterruptedItemSwap(itemDir, meta)
}

// testSwapHook is an optional unit-test fault-injection hook invoked at critical
// rename/commit steps ("backup_v1", "place_v2", "save_manifest") during staged swaps.
var testSwapHook func(step, src, dst string) error

func newSwapToken() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(b[:]))
}

func stagedRelPath(rel string) string {
	return filepath.ToSlash(strings.TrimSpace(rel)) + storage.TempSuffix
}

func isSwapStageDir(dir string) bool {
	return strings.HasPrefix(filepath.Base(filepath.Clean(dir)), library.SwapStageDirPrefix)
}

func stagedFullPath(stageDir, rel string) (string, error) {
	return library.ResolveItemPath(stageDir, stagedRelPath(rel))
}

func wrapRemoteDegraded(err error) error {
	if err == nil {
		return ErrRemoteDegradedRefused
	}
	if errors.Is(err, ErrRemoteDegradedRefused) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrRemoteDegradedRefused, err)
}

func wrapRemoteDegradedMsg(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrRemoteDegradedRefused
	}
	return fmt.Errorf("%w: %s", ErrRemoteDegradedRefused, reason)
}

func isDegradedSiteError(err error) bool {
	if err == nil {
		return false
	}
	if isWeiboPlaceholderError(err) {
		return true
	}
	var encErr *EncryptedMediaError
	if errors.As(err, &encErr) {
		return true
	}
	switch classifyDownloadError(err) {
	case site.ErrKindUnavailable, site.ErrKindForbidden, site.ErrKindBlocked:
		return true
	default:
		return false
	}
}

func (m *Manager) wrapDegradedIfLocalProtected(itemKey, mode string, err error) error {
	if err == nil || m == nil || m.lib == nil {
		return err
	}
	if !isDegradedSiteError(err) {
		return err
	}
	if meta, ok, gErr := m.fetchExistingAnyKind(itemKey); gErr == nil && ok && localHasProtectedContent(meta) {
		_ = mode
		return wrapRemoteDegraded(err)
	}
	return err
}

func (m *Manager) fetchExistingAnyKind(itemKey string) (library.ItemMetadata, bool, error) {
	if m == nil || m.lib == nil || strings.TrimSpace(itemKey) == "" {
		return library.ItemMetadata{}, false, nil
	}
	meta, ok, err := m.lib.GetItem(itemKey)
	if err != nil || ok {
		return meta, ok, err
	}
	parts := strings.SplitN(strings.TrimSpace(itemKey), ":", 3)
	if len(parts) == 3 {
		altKind := ""
		switch strings.ToLower(parts[1]) {
		case "video":
			altKind = "dynamic"
		case "dynamic", "note":
			altKind = "video"
		}
		if altKind != "" {
			altKey := library.CanonicalItemKey(parts[0], altKind, parts[2])
			return m.lib.GetItem(altKey)
		}
	}
	return library.ItemMetadata{}, false, nil
}

// localHasValidMedia reports whether meta has at least one verified primary
// media file (video, image, motion, audio) on disk in meta.ItemDir.
func localHasValidMedia(meta library.ItemMetadata) bool {
	if strings.TrimSpace(meta.ItemDir) == "" {
		return false
	}
	for _, mf := range meta.Media {
		switch mf.Role {
		case "video", "image", "motion", "audio":
			if library.VerifyMediaFile(meta.ItemDir, mf, true) {
				return true
			}
		}
	}
	for _, p := range meta.Parts {
		if p.File != "" && mediaIsValid(meta, p.File) {
			return true
		}
	}
	if meta.DouyinVideo != nil && meta.DouyinVideo.File != "" && mediaIsValid(meta, meta.DouyinVideo.File) {
		return true
	}
	for _, np := range meta.NotePages {
		if (np.Image != "" && mediaIsValid(meta, np.Image)) ||
			(np.Video != "" && mediaIsValid(meta, np.Video)) ||
			(np.Motion != "" && mediaIsValid(meta, np.Motion)) {
			return true
		}
	}
	return false
}

// localHasProtectedContent reports whether meta represents an existing cached
// item with valid local media or a completed text post that must never be
// silently overwritten by a degraded remote response or V2 edit.
func localHasProtectedContent(meta library.ItemMetadata) bool {
	if localHasValidMedia(meta) {
		return true
	}
	if meta.Status == "complete" && strings.TrimSpace(meta.ItemDir) != "" {
		if _, err := os.Stat(filepath.Join(meta.ItemDir, "item.json")); err == nil {
			return true
		}
	}
	return false
}

func stagedMetaHasPrimaryMedia(meta library.ItemMetadata, stageDir string) bool {
	for _, mf := range meta.Media {
		switch mf.Role {
		case "video", "image", "motion", "audio":
			if p, err := stagedFullPath(stageDir, mf.Path); err == nil {
				if info, sErr := os.Stat(p); sErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
					return true
				}
			}
		}
	}
	return false
}

func copyVerifiedFileToStage(srcItemDir, rel, wantSHA, stageDir string) (int64, string, error) {
	srcFull, err := library.ResolveItemPath(srcItemDir, rel)
	if err != nil {
		return 0, "", err
	}
	dstFull, err := stagedFullPath(stageDir, rel)
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(dstFull), 0o700); err != nil {
		return 0, "", err
	}
	in, err := os.Open(srcFull)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()

	tmp := storage.TempSiblingPath(dstFull)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	cleanup := func() {
		_ = out.Close()
		_ = os.Remove(tmp)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if err != nil {
		cleanup()
		return 0, "", err
	}
	if n <= 0 {
		cleanup()
		return 0, "", fmt.Errorf("download: 本地缓存文件为空 (%s)", rel)
	}
	if err := out.Sync(); err != nil {
		cleanup()
		return 0, "", err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, "", err
	}
	gotSHA := hex.EncodeToString(h.Sum(nil))
	if wantSHA != "" && !strings.EqualFold(gotSHA, wantSHA) {
		_ = os.Remove(tmp)
		return 0, "", fmt.Errorf("download: 本地缓存文件校验不匹配 (%s)", rel)
	}
	if err := os.Rename(tmp, dstFull); err != nil {
		_ = os.Remove(tmp)
		return 0, "", err
	}
	return n, gotSHA, nil
}

func writeBytesToStage(stageDir, rel string, data []byte) (int64, string, error) {
	dstFull, err := stagedFullPath(stageDir, rel)
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(dstFull), 0o700); err != nil {
		return 0, "", err
	}
	if err := storage.WriteFileAtomic(dstFull, data, 0o600); err != nil {
		return 0, "", err
	}
	sum := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(sum[:]), nil
}

func moveLiveStageOutputToSwapStage(srcPath, stageDir, rel string) error {
	dstFull, err := stagedFullPath(stageDir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstFull), 0o700); err != nil {
		return err
	}
	return os.Rename(srcPath, dstFull)
}

func stampCacheStandardAndAssets(meta *library.ItemMetadata, editTs int64, editCount int, assetIDs []string) {
	if meta == nil {
		return
	}
	if editTs > 0 {
		meta.EditTs = editTs
	}
	if editCount > 0 {
		meta.EditCount = editCount
	}
	if len(assetIDs) > 0 {
		cleaned := make([]string, 0, len(assetIDs))
		for _, raw := range assetIDs {
			if tok := library.SanitizeMediaToken(raw); tok != "" {
				cleaned = append(cleaned, tok)
			}
		}
		if len(cleaned) > 0 {
			meta.AssetIDs = cleaned
		}
	}
	targetStd := library.SiteCacheStandardVersion(meta.Site, meta.Kind)
	// Weibo v4 and Bilibili video v3 have conditional stamping rules (handled by
	// weibo_plan.go after /oslarge/ deferral checks and video.go after saveVideoExtras).
	if strings.EqualFold(meta.Site, "weibo") ||
		(strings.EqualFold(meta.Site, "bilibili") && strings.EqualFold(meta.Kind, "video")) {
		targetStd = library.CurrentCacheStandardVersion
	}
	if meta.Status == "complete" && len(meta.Missing) == 0 && meta.CacheStandardVersion < targetStd {
		meta.CacheStandardVersion = targetStd
	}
}

// commitStagedItemSwap verifies 100% of v2Meta's registered files inside stageDir
// ("<stageDir>/<rel>.part"), optionally archives V1 via ArchiveItemSnapshot when
// mode == ConflictResolveKeepBoth, moves replaced V1 files to ".trace-prev-<token>/",
// renames staged V2 files into itemDir, commits SaveItem(itemDir, v2Meta), and
// cleans up ".trace-prev-*" + stale V1-only files. On ANY failure (including a
// Windows file sharing violation), it immediately rolls back V1 from ".trace-prev-*".
func (m *Manager) commitStagedItemSwap(
	itemDir string,
	v1Meta library.ItemMetadata,
	existsV1 bool,
	v2Meta library.ItemMetadata,
	stageDir string,
	mode string,
) (library.ItemMetadata, error) {
	itemDir = filepath.Clean(itemDir)
	stageDir = filepath.Clean(stageDir)
	defer func() {
		_ = library.RemoveTempSubdirSafe(itemDir, stageDir)
	}()

	if err := library.RejectDirectoryLinks(itemDir); err != nil {
		return library.ItemMetadata{}, err
	}
	if err := library.RejectDirectoryLinks(stageDir); err != nil {
		return library.ItemMetadata{}, err
	}

	if existsV1 && localHasValidMedia(v1Meta) && !stagedMetaHasPrimaryMedia(v2Meta, stageDir) {
		return library.ItemMetadata{}, wrapRemoteDegradedMsg("源站未返回可用媒体文件，已保留本地原文件")
	}

	v2Files := library.RegisteredFiles(v2Meta)
	hasOrigSnap := false
	for _, mf := range v2Files {
		if strings.EqualFold(mf.Path, "orig.json") {
			hasOrigSnap = true
			break
		}
	}
	if !hasOrigSnap {
		if snapPath, sErr := stagedFullPath(stageDir, "orig.json"); sErr == nil {
			if info, stErr := os.Lstat(snapPath); stErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
				v2Files = append(v2Files, library.ManifestFile{Path: "orig.json", Role: "snapshot"})
			}
		}
	}

	type verifiedStaged struct {
		rel        string
		stagedFull string
		liveFull   string
		bytes      int64
		sha256     string
	}
	stagedList := make([]verifiedStaged, 0, len(v2Files))
	digests := make(map[string]verifiedStaged, len(v2Files))
	for _, mf := range v2Files {
		sFull, err := stagedFullPath(stageDir, mf.Path)
		if err != nil {
			return library.ItemMetadata{}, err
		}
		lFull, err := library.ResolveItemPath(itemDir, mf.Path)
		if err != nil {
			return library.ItemMetadata{}, err
		}
		info, err := os.Lstat(sFull)
		if err != nil {
			return library.ItemMetadata{}, fmt.Errorf("download: 暂存文件缺失 (%s): %w", mf.Path, err)
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return library.ItemMetadata{}, fmt.Errorf("download: 暂存文件无效 (%s)", mf.Path)
		}
		shaHex, actualSize, err := hashFileSHA256(sFull)
		if err != nil {
			return library.ItemMetadata{}, err
		}
		if mf.Bytes > 0 && actualSize != mf.Bytes {
			return library.ItemMetadata{}, fmt.Errorf("download: 暂存文件大小不一致 (%s)", mf.Path)
		}
		if mf.SHA256 != "" && !strings.EqualFold(shaHex, mf.SHA256) {
			return library.ItemMetadata{}, fmt.Errorf("download: 暂存文件哈希不一致 (%s)", mf.Path)
		}
		vs := verifiedStaged{
			rel:        mf.Path,
			stagedFull: sFull,
			liveFull:   lFull,
			bytes:      actualSize,
			sha256:     shaHex,
		}
		stagedList = append(stagedList, vs)
		digests[strings.ToLower(filepath.ToSlash(mf.Path))] = vs
	}

	for i := range v2Meta.Media {
		key := strings.ToLower(filepath.ToSlash(strings.TrimSpace(v2Meta.Media[i].Path)))
		if vs, ok := digests[key]; ok {
			v2Meta.Media[i].Bytes = vs.bytes
			v2Meta.Media[i].SHA256 = vs.sha256
		}
	}

	// Refuse to clobber any unregistered user-placed file at a new V2 target path.
	v1RegSet := make(map[string]bool)
	var v1Rels []string
	if existsV1 {
		for _, mf := range library.RegisteredFiles(v1Meta) {
			k := strings.ToLower(filepath.ToSlash(mf.Path))
			if !v1RegSet[k] {
				v1RegSet[k] = true
				v1Rels = append(v1Rels, mf.Path)
			}
		}
		if !v1RegSet["orig.json"] {
			if origSnap, err := library.ResolveItemPath(itemDir, "orig.json"); err == nil {
				if info, sErr := os.Lstat(origSnap); sErr == nil && info.Mode().IsRegular() {
					v1RegSet["orig.json"] = true
					v1Rels = append(v1Rels, "orig.json")
				}
			}
		}
	}
	for _, vs := range stagedList {
		k := strings.ToLower(filepath.ToSlash(vs.rel))
		if !v1RegSet[k] {
			if _, statErr := os.Lstat(vs.liveFull); statErr == nil {
				return library.ItemMetadata{}, &site.Error{
					Site:    site.ID(v2Meta.Site),
					Kind:    site.ErrKindStorage,
					Message: "目标文件已存在且未由本地清单登记，已保留原文件",
				}
			}
		}
	}

	var archivedMeta library.ItemMetadata
	var didArchive bool
	if existsV1 && normalizeConflictResolution(mode) == ConflictResolveKeepBoth {
		arch, err := m.lib.ArchiveItemSnapshot(v1Meta.ItemKey, time.Now().UTC())
		if err != nil {
			return library.ItemMetadata{}, err
		}
		archivedMeta = arch
		didArchive = true
	}

	token := strings.TrimPrefix(filepath.Base(stageDir), library.SwapStageDirPrefix)
	if token == "" || token == filepath.Base(stageDir) {
		token = newSwapToken()
	}
	prevDir := filepath.Join(itemDir, library.SwapPrevDirPrefix+token)
	if err := os.MkdirAll(prevDir, 0o700); err != nil {
		if didArchive {
			_, _ = m.lib.Delete(archivedMeta.ItemKey)
		}
		return library.ItemMetadata{}, err
	}
	if err := library.RejectDirectoryLinks(prevDir); err != nil {
		_ = library.RemoveTempSubdirSafe(itemDir, prevDir)
		if didArchive {
			_, _ = m.lib.Delete(archivedMeta.ItemKey)
		}
		return library.ItemMetadata{}, err
	}

	type movedBackup struct {
		rel      string
		liveFull string
		prevFull string
	}
	var movedV1 []movedBackup
	var placedV2 []string
	kindFlipped := existsV1 && v1Meta.ItemKey != "" &&
		library.CanonicalItemKey(v2Meta.Site, v2Meta.Kind, library.NormalizeRawID(v2Meta.Kind, v2Meta.ID)) != v1Meta.ItemKey

	rollback := func() {
		for i := len(placedV2) - 1; i >= 0; i-- {
			_ = os.Remove(placedV2[i])
		}
		allRestored := true
		for i := len(movedV1) - 1; i >= 0; i-- {
			mb := movedV1[i]
			_ = os.MkdirAll(filepath.Dir(mb.liveFull), 0o700)
			if rErr := os.Rename(mb.prevFull, mb.liveFull); rErr != nil {
				allRestored = false
			}
		}
		if allRestored {
			_ = library.RemoveTempSubdirSafe(itemDir, prevDir)
		}
		if didArchive {
			_, _ = m.lib.Delete(archivedMeta.ItemKey)
		}
	}

	// Move existing V1 registered files (and item.json if Kind flipped) into prevDir.
	for _, rel := range v1Rels {
		liveFull, err := library.ResolveItemPath(itemDir, rel)
		if err != nil {
			rollback()
			return library.ItemMetadata{}, err
		}
		if _, statErr := os.Lstat(liveFull); os.IsNotExist(statErr) {
			continue
		} else if statErr != nil {
			rollback()
			return library.ItemMetadata{}, statErr
		}
		prevFull, err := library.ResolveItemPath(prevDir, stagedRelPath(rel))
		if err != nil {
			rollback()
			return library.ItemMetadata{}, err
		}
		if err := os.MkdirAll(filepath.Dir(prevFull), 0o700); err != nil {
			rollback()
			return library.ItemMetadata{}, err
		}
		if hook := testSwapHook; hook != nil {
			if hErr := hook("backup_v1", liveFull, prevFull); hErr != nil {
				rollback()
				return library.ItemMetadata{}, hErr
			}
		}
		if err := os.Rename(liveFull, prevFull); err != nil {
			rollback()
			return library.ItemMetadata{}, err
		}
		movedV1 = append(movedV1, movedBackup{rel: rel, liveFull: liveFull, prevFull: prevFull})
	}

	if kindFlipped {
		liveManifest := filepath.Join(itemDir, "item.json")
		if _, statErr := os.Lstat(liveManifest); statErr == nil {
			prevManifest := filepath.Join(prevDir, "item.json"+storage.TempSuffix)
			if err := os.Rename(liveManifest, prevManifest); err != nil {
				rollback()
				return library.ItemMetadata{}, err
			}
			movedV1 = append(movedV1, movedBackup{rel: "item.json", liveFull: liveManifest, prevFull: prevManifest})
		}
	}

	// Place verified V2 files into itemDir.
	for _, vs := range stagedList {
		if err := os.MkdirAll(filepath.Dir(vs.liveFull), 0o700); err != nil {
			rollback()
			return library.ItemMetadata{}, err
		}
		if err := library.RejectDirectoryLinks(filepath.Dir(vs.liveFull)); err != nil {
			rollback()
			return library.ItemMetadata{}, err
		}
		if hook := testSwapHook; hook != nil {
			if hErr := hook("place_v2", vs.stagedFull, vs.liveFull); hErr != nil {
				rollback()
				return library.ItemMetadata{}, hErr
			}
		}
		if err := os.Rename(vs.stagedFull, vs.liveFull); err != nil {
			rollback()
			return library.ItemMetadata{}, err
		}
		placedV2 = append(placedV2, vs.liveFull)
	}

	swapTargetStd := library.SiteCacheStandardVersion(v2Meta.Site, v2Meta.Kind)
	if strings.EqualFold(v2Meta.Site, "weibo") {
		swapTargetStd = library.CurrentCacheStandardVersion
	}
	if v2Meta.Status == "complete" && len(v2Meta.Missing) == 0 && v2Meta.CacheStandardVersion < swapTargetStd {
		v2Meta.CacheStandardVersion = swapTargetStd
	}
	if hook := testSwapHook; hook != nil {
		if hErr := hook("save_manifest", itemDir, filepath.Join(itemDir, "item.json")); hErr != nil {
			rollback()
			return library.ItemMetadata{}, hErr
		}
	}
	saved, err := m.lib.SaveItem(itemDir, v2Meta)
	if err != nil {
		rollback()
		return library.ItemMetadata{}, err
	}
	if kindFlipped {
		_ = m.lib.DeleteItem(v1Meta.ItemKey, false)
	}

	_ = library.RemoveTempSubdirSafe(itemDir, prevDir)
	removeEmptyItemSubdirs(itemDir)
	return saved, nil
}

func removeEmptyItemSubdirs(itemDir string) {
	itemDir = filepath.Clean(itemDir)
	entries, err := os.ReadDir(itemDir)
	if err != nil {
		return
	}
	for _, d := range entries {
		if !d.IsDir() || storage.IsTempName(d.Name()) || library.IsLinkLike(d.Type()) {
			continue
		}
		sub := filepath.Join(itemDir, d.Name())
		subEntries, sErr := os.ReadDir(sub)
		if sErr == nil && len(subEntries) == 0 {
			_ = os.Remove(sub)
		}
	}
}

// --- Sanitization & Fingerprinting Helpers ---

var (
	reConflictURL     = regexp.MustCompile(`(?i)https?://\S+|//\S+`)
	reConflictSpace   = regexp.MustCompile(`\s+`)
	reQueryKeyValSpan = regexp.MustCompile(`(?i)\b[a-z0-9_-]{2,32}=[^\s,&;]+`)
)

func sanitizeConflictText(raw string, maxRunes int) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = reConflictURL.ReplaceAllString(s, "")
	s = reQueryKeyValSpan.ReplaceAllString(s, "")
	for _, bad := range []string{
		"weibocdn.com", "sinaimg.cn", "twimg.com", "douyinvod.com", "byteimg.com",
		"hdslb.com", "bilivideo.com", "pscp.tv", "periscope.tv",
		"sessdata", "bili_jct", "sessionid", "auth_token", "ct0", "wbpsess", "xsrf-token",
		"a_bogus", "x-bogus", "x-signature", "ssig", "upsig", "w_rid",
	} {
		lower := strings.ToLower(s)
		for {
			idx := strings.Index(lower, bad)
			if idx < 0 {
				break
			}
			s = s[:idx] + s[idx+len(bad):]
			lower = strings.ToLower(s)
		}
	}
	s = strings.NewReplacer("?", " ", "&", " ", "=", " ").Replace(s)
	s = strings.TrimSpace(reConflictSpace.ReplaceAllString(s, " "))
	if maxRunes > 0 {
		runes := []rune(s)
		if len(runes) > maxRunes {
			s = strings.TrimSpace(string(runes[:maxRunes])) + "…"
		}
	}
	return s
}

func sanitizeConflictNotice(n ConflictNotice) ConflictNotice {
	if canon, ok := library.ValidateCanonicalItemKey(n.ItemKey); ok {
		n.ItemKey = canon
	} else {
		n.ItemKey = sanitizeConflictText(n.ItemKey, 120)
	}
	n.Site = strings.ToLower(strings.TrimSpace(n.Site))
	n.Kind = strings.ToLower(strings.TrimSpace(n.Kind))
	n.TargetID = sanitizeConflictText(n.TargetID, 80)
	n.AuthorUID = sanitizeConflictText(n.AuthorUID, 120)
	n.AuthorName = sanitizeConflictText(n.AuthorName, 80)
	n.Title = sanitizeConflictText(n.Title, 120)
	n.Origin = sanitizeConflictText(n.Origin, 32)
	switch n.ReasonKind {
	case ConflictReasonParts, ConflictReasonMedia, ConflictReasonText, ConflictReasonMismatch:
	default:
		n.ReasonKind = ConflictReasonMismatch
	}
	n.LocalSummary = sanitizeConflictText(n.LocalSummary, 160)
	n.RemoteSummary = sanitizeConflictText(n.RemoteSummary, 160)
	fp := strings.ToLower(strings.TrimSpace(n.RemoteFingerprint))
	if len(fp) != 64 || !isLowerHex(fp) {
		sum := sha256.Sum256([]byte(n.ItemKey + "|" + n.ReasonKind + "|" + n.RemoteSummary))
		fp = hex.EncodeToString(sum[:])
	}
	n.RemoteFingerprint = fp
	return n
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

func computeFingerprint(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			_, _ = h.Write([]byte("|"))
		}
		_, _ = h.Write([]byte(strings.TrimSpace(p)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func conflictOriginFromTask(t *Task) string {
	if t == nil {
		return "download"
	}
	switch strings.ToLower(strings.TrimSpace(t.Source)) {
	case "queue":
		return "full_cache"
	case "auto":
		return "auto_cache"
	case "manual", "":
		return "manual"
	default:
		return "download"
	}
}

func durationChangedSubstantively(localSec, remoteSec int) bool {
	if localSec <= 0 || remoteSec <= 0 {
		return false
	}
	diff := math.Abs(float64(localSec - remoteSec))
	tol := math.Max(2.0, float64(localSec)*0.03)
	return diff > tol
}

func normalizeCompareText(s string) string {
	return NormalizeConflictText(s)
}

func looksLikeTruncatedPrefix(shortText, fullText string) bool {
	return LooksLikeTruncatedTextPrefix(shortText, fullText)
}

func preserveMonotonicText(localText, remoteText string, editAdvanced bool) string {
	if editAdvanced {
		return remoteText
	}
	if strings.TrimSpace(remoteText) == "" && strings.TrimSpace(localText) != "" {
		return localText
	}
	if looksLikeTruncatedPrefix(remoteText, localText) {
		return localText
	}
	return remoteText
}

// --- Bilibili Video & Dynamic Classification + Staged Execution ---

func bilibiliVideoAssetIDsFromPages(pages []bilibili.VideoPage) []string {
	out := make([]string, 0, len(pages))
	for _, p := range pages {
		if p.CID > 0 {
			if tok := library.SanitizeMediaToken(fmt.Sprintf("P%02d_%d", p.Page, p.CID)); tok != "" {
				out = append(out, tok)
			}
		}
	}
	return out
}

func bilibiliVideoAssetIDsFromParts(parts []library.VideoPartRecord) []string {
	sorted := append([]library.VideoPartRecord(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Page < sorted[j].Page })
	out := make([]string, 0, len(sorted))
	for _, p := range sorted {
		if p.CID > 0 {
			if tok := library.SanitizeMediaToken(fmt.Sprintf("P%02d_%d", p.Page, p.CID)); tok != "" {
				out = append(out, tok)
			}
		}
	}
	return out
}

func classifyBilibiliVideoVsLocal(local library.ItemMetadata, detail bilibili.VideoDetail, t *Task) (ConflictNotice, bool, error) {
	if !localHasProtectedContent(local) {
		return ConflictNotice{}, false, nil
	}
	if len(detail.Pages) == 0 && localHasValidMedia(local) {
		return ConflictNotice{}, false, wrapRemoteDegradedMsg("源站投稿分 P 列表为空，已保留本地原文件")
	}

	remoteByPage := make(map[int]bilibili.VideoPage, len(detail.Pages))
	remoteByCID := make(map[int64]bilibili.VideoPage, len(detail.Pages))
	var remoteTokens []string
	for _, vp := range detail.Pages {
		remoteByPage[vp.Page] = vp
		if vp.CID > 0 {
			remoteByCID[vp.CID] = vp
		}
		remoteTokens = append(remoteTokens, fmt.Sprintf("P%02d:%d:%d", vp.Page, vp.CID, vp.Duration))
	}

	var cachedParts []library.VideoPartRecord
	for _, p := range local.Parts {
		if p.File != "" && mediaIsValid(local, p.File) {
			cachedParts = append(cachedParts, p)
		}
	}

	reasonKind := ""
	var diffDetail string
	for _, lp := range cachedParts {
		rp, hasPage := remoteByPage[lp.Page]
		if !hasPage {
			reasonKind = ConflictReasonParts
			diffDetail = fmt.Sprintf("源站已移除第 %d P", lp.Page)
			break
		}
		if lp.CID > 0 && rp.CID != lp.CID {
			if moved, wasMoved := remoteByCID[lp.CID]; wasMoved && moved.Page != lp.Page {
				reasonKind = ConflictReasonParts
				diffDetail = fmt.Sprintf("分 P 顺序变更（原 P%02d 移至 P%02d）", lp.Page, moved.Page)
			} else {
				reasonKind = ConflictReasonParts
				diffDetail = fmt.Sprintf("P%02d 已换源重传（CID 变更）", lp.Page)
			}
			break
		}
		if durationChangedSubstantively(lp.Duration, rp.Duration) {
			reasonKind = ConflictReasonParts
			diffDetail = fmt.Sprintf("P%02d 时长变化（%ds -> %ds）", lp.Page, lp.Duration, rp.Duration)
			break
		}
	}

	if reasonKind == "" && len(cachedParts) > 0 && len(detail.Pages) < len(local.ExpectedPages) {
		reasonKind = ConflictReasonParts
		diffDetail = fmt.Sprintf("分 P 数量减少（%dP -> %dP）", len(local.ExpectedPages), len(detail.Pages))
	}

	if reasonKind == "" {
		return ConflictNotice{}, false, nil
	}

	localSum := fmt.Sprintf("已缓存 %d 个分 P（%s）", max(1, len(cachedParts)), firstNonEmptyStr(local.Title, detail.BVID))
	remoteSum := fmt.Sprintf("源站当前 %d 个分 P，%s", len(detail.Pages), diffDetail)
	_ = remoteTokens
	fp := BilibiliVideoConflictFingerprint(detail.BVID, detail.Pages, detail.Title, detail.Desc)
	return ConflictNotice{
		ItemKey:           library.CanonicalItemKey("bilibili", "video", detail.BVID),
		Site:              "bilibili",
		Kind:              "video",
		TargetID:          detail.BVID,
		AuthorUID:         strconv.FormatInt(detail.Author.Mid, 10),
		AuthorName:        detail.Author.Name,
		Title:             firstNonEmptyStr(detail.Title, local.Title, detail.BVID),
		Origin:            conflictOriginFromTask(t),
		ReasonKind:        reasonKind,
		LocalSummary:      localSum,
		RemoteSummary:     remoteSum,
		RemoteFingerprint: fp,
		Via:               local.Via,
	}, true, nil
}

func (m *Manager) executeBilibiliVideoConflictReplace(
	ctx context.Context,
	t *Task,
	detail bilibili.VideoDetail,
	localMeta library.ItemMetadata,
	mode string,
	selectedPages []int,
) error {
	if len(detail.Pages) == 0 {
		return wrapRemoteDegradedMsg("源站投稿无可用分 P")
	}
	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()

	pageFilter := make(map[int]bool, len(selectedPages))
	for _, p := range selectedPages {
		pageFilter[p] = true
	}
	var targetPages []bilibili.VideoPage
	for _, vp := range detail.Pages {
		if len(pageFilter) > 0 {
			if pageFilter[vp.Page] {
				targetPages = append(targetPages, vp)
			}
			continue
		}
		if cfg.MultiPart == "first" && vp.Page != 1 {
			continue
		}
		targetPages = append(targetPages, vp)
	}
	if len(targetPages) == 0 {
		targetPages = []bilibili.VideoPage{detail.Pages[0]}
	}

	itemDir := localMeta.ItemDir
	stageDir := filepath.Join(itemDir, library.SwapStageDirPrefix+newSwapToken())
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return err
	}
	defer func() {
		_ = library.RemoveTempSubdirSafe(itemDir, stageDir)
	}()

	localByCID := make(map[int64]library.VideoPartRecord, len(localMeta.Parts))
	for _, lp := range localMeta.Parts {
		if lp.CID > 0 && lp.File != "" && mediaIsValid(localMeta, lp.File) {
			localByCID[lp.CID] = lp
		}
	}
	localMediaByPath := make(map[string]library.MediaFile, len(localMeta.Media))
	for _, mf := range localMeta.Media {
		localMediaByPath[mf.Path] = mf
	}

	var v2Media []library.MediaFile
	var v2Parts []library.VideoPartRecord
	var v2Missing []library.MissingRecord
	var expectedPages []int
	for _, vp := range targetPages {
		expectedPages = append(expectedPages, vp.Page)
	}

	forceOverwrite := effectiveForceOverwrite(ctx, t.Source, t.ForceOverwrite)

	// Stage cover if enabled.
	if cfg.SaveCover && detail.Cover != "" {
		coverRel := "cover" + guessImageExt(detail.Cover)
		coverStaged, err := stagedFullPath(stageDir, coverRel)
		if err != nil {
			return err
		}
		stageTask := cloneStageSubtask(t, stageDir)
		n, sha, cErr := m.downloadFileWithRange(ctx, []string{detail.Cover}, coverStaged, &stageTask)
		if cErr == nil {
			v2Media = append(v2Media, library.MediaFile{Role: "cover", Path: coverRel, Bytes: n, SHA256: sha, Source: detail.Cover})
		} else if isStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
			return cErr
		} else {
			// Reuse verified local cover if present (never on manual ForceOverwrite).
			reused := false
			if !forceOverwrite {
				for _, mf := range localMeta.Media {
					if mf.Role == "cover" && mediaIsValid(localMeta, mf.Path) {
						if cn, csha, rErr := copyVerifiedFileToStage(itemDir, mf.Path, mf.SHA256, stageDir); rErr == nil {
							cp := mf
							cp.Bytes, cp.SHA256 = cn, csha
							v2Media = append(v2Media, cp)
							reused = true
						}
						break
					}
				}
			}
			if !reused {
				v2Missing = append(v2Missing, library.MissingRecord{Item: "cover", Reason: "封面保存失败 (" + bilibili.KindOf(cErr) + ")"})
			}
		}
	}

	opts := bilibili.PlaySelectionOptions{
		PreferredCodec:      cfg.PreferredVideoCodec,
		CodecOrder:          cfg.CodecOrder,
		PreferLosslessAudio: cfg.PreferLosslessAudio,
		PreferDolbyAudio:    cfg.PreferDolbyAudio,
		TargetQn:            targetQuality(cfg.VideoQuality),
	}

	reuseLocalPartToStage := func(vp bilibili.VideoPage, lp library.VideoPartRecord) (library.VideoPartRecord, []library.MediaFile, error) {
		mp4Rel := fmt.Sprintf("P%02d.mp4", vp.Page)
		srcMF := localMediaByPath[lp.File]
		n, sha, err := copyVerifiedFileToStage(itemDir, lp.File, srcMF.SHA256, stageDir)
		if err != nil {
			return library.VideoPartRecord{}, nil, err
		}
		outPart := lp
		outPart.Page = vp.Page
		outPart.Title = firstNonEmptyStr(vp.Title, lp.Title)
		outPart.File = mp4Rel
		var stagedFiles []library.MediaFile
		vidMF := srcMF
		vidMF.Role = "video"
		vidMF.Path = mp4Rel
		vidMF.Bytes = n
		vidMF.SHA256 = sha
		if vidMF.Duration == 0 {
			vidMF.Duration = lp.Duration
		}
		stagedFiles = append(stagedFiles, vidMF)

		// Copy verified sidecar audio if present.
		if lp.Audio.SavedAs != "" && lp.Audio.SavedAs != lp.File && mediaIsValid(localMeta, lp.Audio.SavedAs) {
			audRel := fmt.Sprintf("P%02d.audio-%d.m4a", vp.Page, lp.Audio.Quality)
			aMF := localMediaByPath[lp.Audio.SavedAs]
			if an, asha, aErr := copyVerifiedFileToStageFromDstRel(itemDir, lp.Audio.SavedAs, aMF.SHA256, stageDir, audRel); aErr == nil {
				outPart.Audio.SavedAs = audRel
				stagedFiles = append(stagedFiles, library.MediaFile{Role: "audio", Path: audRel, Bytes: an, SHA256: asha})
			}
		} else if outPart.Audio.SavedAs == lp.File {
			outPart.Audio.SavedAs = mp4Rel
		}

		// Copy verified danmaku snapshot if present.
		if lp.Danmaku != "" && mediaIsValid(localMeta, lp.Danmaku) {
			dmRel := fmt.Sprintf("P%02d.danmaku.xml", vp.Page)
			dMF := localMediaByPath[lp.Danmaku]
			if dn, dsha, dErr := copyVerifiedFileToStageFromDstRel(itemDir, lp.Danmaku, dMF.SHA256, stageDir, dmRel); dErr == nil {
				outPart.Danmaku = dmRel
				outPart.DanmakuState = "saved"
				stagedFiles = append(stagedFiles, library.MediaFile{Role: "danmaku", Path: dmRel, Bytes: dn, SHA256: dsha})
			}
		}

		// Copy verified subtitles if present.
		if len(lp.SubtitleTracks) > 0 {
			var keptSubs []string
			var keptTracks []library.PartSubtitleInfo
			for _, tr := range lp.SubtitleTracks {
				lang := library.SanitizeFileName(tr.Language)
				if lang == "" {
					lang = "und"
				}
				vttDst := fmt.Sprintf("P%02d.%s.vtt", vp.Page, lang)
				srtDst := fmt.Sprintf("P%02d.%s.srt", vp.Page, lang)
				if mediaIsValid(localMeta, tr.VTT) && mediaIsValid(localMeta, tr.SRT) {
					vMF, sMF := localMediaByPath[tr.VTT], localMediaByPath[tr.SRT]
					vn, vsha, vErr := copyVerifiedFileToStageFromDstRel(itemDir, tr.VTT, vMF.SHA256, stageDir, vttDst)
					sn, ssha, sErr := copyVerifiedFileToStageFromDstRel(itemDir, tr.SRT, sMF.SHA256, stageDir, srtDst)
					if vErr == nil && sErr == nil {
						keptSubs = append(keptSubs, vttDst, srtDst)
						tr.VTT, tr.SRT = vttDst, srtDst
						keptTracks = append(keptTracks, tr)
						stagedFiles = append(stagedFiles,
							library.MediaFile{Role: "subtitle", Path: vttDst, Bytes: vn, SHA256: vsha},
							library.MediaFile{Role: "subtitle", Path: srtDst, Bytes: sn, SHA256: ssha},
						)
					}
				}
			}
			outPart.Subtitles = keptSubs
			outPart.SubtitleTracks = keptTracks
		}
		return outPart, stagedFiles, nil
	}

	for _, vp := range targetPages {
		if err := ctx.Err(); err != nil {
			return err
		}
		lp, hasSameCID := localByCID[vp.CID]
		partRec, partFiles, dlErr := m.stageSingleBilibiliPart(ctx, t, detail.BVID, vp, opts, cfg, stageDir, hasSameCID, lp, itemDir, localMeta)
		if dlErr != nil {
			// Invariant #3: on mixed V2 edits, if an unchanged CID fails remote download
			// because CDN expired or degraded, reuse the verified local V1 part (never on manual ForceOverwrite).
			if !forceOverwrite && hasSameCID && ctx.Err() == nil && !isStorageError(dlErr) {
				reusedPart, reusedFiles, rErr := reuseLocalPartToStage(vp, lp)
				if rErr == nil {
					v2Parts = append(v2Parts, reusedPart)
					v2Media = append(v2Media, reusedFiles...)
					m.setFileProgress(t, "video", reusedPart.File, "completed", reusedFiles[0].Bytes, reusedFiles[0].Bytes, 0)
					continue
				}
			}
			if isDegradedSiteError(dlErr) {
				return wrapRemoteDegraded(dlErr)
			}
			return dlErr
		}
		v2Parts = append(v2Parts, partRec)
		v2Media = append(v2Media, partFiles...)
		if forceOverwrite && partRec.DanmakuState == "missing" {
			v2Missing = append(v2Missing, library.MissingRecord{
				Item:   fmt.Sprintf("P%02d.danmaku.xml", vp.Page),
				Reason: "弹幕下载失败",
			})
		}
	}

	status := "complete"
	if len(v2Missing) > 0 {
		status = "partial"
		t.Status = "partial"
	}
	v2Meta := library.ItemMetadata{
		Version:         1,
		ID:              "video:" + detail.BVID,
		Site:            "bilibili",
		Kind:            "video",
		Status:          status,
		Title:           detail.Title,
		Desc:            detail.Desc,
		URL:             detail.CanonicalURL,
		PubTs:           detail.PubTs,
		CachedAt:        localMeta.CachedAt,
		Duration:        detail.Duration,
		Author:          library.ItemAuthor{Mid: detail.Author.Mid, Name: detail.Author.Name, Face: detail.Author.Face},
		Media:           v2Media,
		Parts:           v2Parts,
		ExpectedPages:   expectedPages,
		RemotePartCount: len(detail.Pages),
		Missing:         v2Missing,
		DirectCached:    localMeta.DirectCached || !t.LinkedVideo,
		Via:             localMeta.Via,
		AssetIDs:        bilibiliVideoAssetIDsFromParts(v2Parts),
	}
	for _, p := range v2Parts {
		if bilibili.QualityRank(p.Available.BestVideo) > bilibili.QualityRank(p.Video.Quality) ||
			audioRank(p.Available.BestAudio) > audioRank(p.Audio.Quality) {
			v2Meta.UpgradeAvailable = true
			v2Meta.UpgradeHint = "当前缓存低于站点可提供的最高档"
			break
		}
	}

	saved, err := m.commitStagedItemSwap(itemDir, localMeta, true, v2Meta, stageDir, mode)
	if err != nil {
		return err
	}
	t.ItemKey = saved.ItemKey
	return nil
}

func copyVerifiedFileToStageFromDstRel(srcItemDir, srcRel, wantSHA, stageDir, dstRel string) (int64, string, error) {
	srcFull, err := library.ResolveItemPath(srcItemDir, srcRel)
	if err != nil {
		return 0, "", err
	}
	dstFull, err := stagedFullPath(stageDir, dstRel)
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(dstFull), 0o700); err != nil {
		return 0, "", err
	}
	in, err := os.Open(srcFull)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()

	tmp := storage.TempSiblingPath(dstFull)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	cleanup := func() {
		_ = out.Close()
		_ = os.Remove(tmp)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if err != nil || n <= 0 {
		cleanup()
		if err != nil {
			return 0, "", err
		}
		return 0, "", fmt.Errorf("empty file %s", srcRel)
	}
	if err := out.Sync(); err != nil {
		cleanup()
		return 0, "", err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, "", err
	}
	gotSHA := hex.EncodeToString(h.Sum(nil))
	if wantSHA != "" && !strings.EqualFold(gotSHA, wantSHA) {
		_ = os.Remove(tmp)
		return 0, "", fmt.Errorf("sha256 mismatch for %s", srcRel)
	}
	if err := os.Rename(tmp, dstFull); err != nil {
		_ = os.Remove(tmp)
		return 0, "", err
	}
	return n, gotSHA, nil
}

func cloneStageSubtask(parent *Task, stageDir string) Task {
	var sub Task
	if parent != nil {
		sub = *parent
		sub.TaskID = ""
		sub.Files = nil
		sub.ProgressOwner = parent
		sub.Attempts = parent.Attempts
	}
	sub.ItemDir = stageDir
	return sub
}

// swapVerifiedFileInPlace atomically swaps a verified new file (currently at
// stagedReadyPath, e.g. mp4Path+".part") over liveRel inside itemDir by moving
// any existing live file into ".trace-prev-<token>/<liveRel>.part" first,
// running commitManifest, and rolling back immediately if any rename or manifest
// write fails.
func swapVerifiedFileInPlace(itemDir, liveRel, stagedReadyPath string, commitManifest func() error) error {
	itemDir = filepath.Clean(itemDir)
	liveFull, err := library.ResolveItemPath(itemDir, liveRel)
	if err != nil {
		return err
	}
	hasOld := false
	if info, sErr := os.Lstat(liveFull); sErr == nil && !info.IsDir() {
		hasOld = true
	} else if sErr != nil && !os.IsNotExist(sErr) {
		return sErr
	}

	var prevDir, prevFull string
	if hasOld {
		prevDir = filepath.Join(itemDir, library.SwapPrevDirPrefix+newSwapToken())
		if err := os.MkdirAll(prevDir, 0o700); err != nil {
			return err
		}
		prevFull, err = library.ResolveItemPath(prevDir, stagedRelPath(liveRel))
		if err != nil {
			_ = library.RemoveTempSubdirSafe(itemDir, prevDir)
			return err
		}
		if err := os.MkdirAll(filepath.Dir(prevFull), 0o700); err != nil {
			_ = library.RemoveTempSubdirSafe(itemDir, prevDir)
			return err
		}
		if hook := testSwapHook; hook != nil {
			if hErr := hook("backup_v1", liveFull, prevFull); hErr != nil {
				_ = library.RemoveTempSubdirSafe(itemDir, prevDir)
				return hErr
			}
		}
		if err := os.Rename(liveFull, prevFull); err != nil {
			_ = library.RemoveTempSubdirSafe(itemDir, prevDir)
			return err
		}
	}

	restoreOld := func() {
		_ = os.Remove(liveFull)
		if hasOld && prevFull != "" {
			if rErr := os.Rename(prevFull, liveFull); rErr == nil {
				_ = library.RemoveTempSubdirSafe(itemDir, prevDir)
			}
		}
	}

	if hook := testSwapHook; hook != nil {
		if hErr := hook("place_v2", stagedReadyPath, liveFull); hErr != nil {
			_ = os.Remove(stagedReadyPath)
			restoreOld()
			return hErr
		}
	}
	if err := os.Rename(stagedReadyPath, liveFull); err != nil {
		restoreOld()
		return err
	}

	if commitManifest != nil {
		if hook := testSwapHook; hook != nil {
			if hErr := hook("save_manifest", itemDir, filepath.Join(itemDir, "item.json")); hErr != nil {
				restoreOld()
				return hErr
			}
		}
		if err := commitManifest(); err != nil {
			restoreOld()
			return err
		}
	}

	if prevDir != "" {
		_ = library.RemoveTempSubdirSafe(itemDir, prevDir)
	}
	return nil
}

func (m *Manager) stageSingleBilibiliPart(
	ctx context.Context,
	parentTask *Task,
	bvid string,
	page bilibili.VideoPage,
	opts bilibili.PlaySelectionOptions,
	cfg settings.DownloadSettings,
	stageDir string,
	hasSameCID bool,
	localPart library.VideoPartRecord,
	localItemDir string,
	localMeta library.ItemMetadata,
) (library.VideoPartRecord, []library.MediaFile, error) {
	plan, err := m.client.PlayURL(ctx, bvid, page.CID, opts)
	if err != nil {
		return library.VideoPartRecord{}, nil, err
	}
	if hasSameCID && localPart.Video.Quality > 0 {
		if bilibili.QualityRank(plan.Video.Quality) < bilibili.QualityRank(localPart.Video.Quality) {
			return library.VideoPartRecord{}, nil, wrapRemoteDegradedMsg("源站返回画质低于本地已缓存版本")
		}
	}

	mp4Rel := fmt.Sprintf("P%02d.mp4", page.Page)
	mp4Staged, err := stagedFullPath(stageDir, mp4Rel)
	if err != nil {
		return library.VideoPartRecord{}, nil, err
	}
	subTask := cloneStageSubtask(parentTask, stageDir)

	var refreshed *bilibili.PlayPlan
	refresh := func() (bilibili.PlayPlan, error) {
		if refreshed != nil {
			return *refreshed, nil
		}
		fresh, rErr := m.client.PlayURL(ctx, bvid, page.CID, opts)
		if rErr == nil {
			refreshed = &fresh
		}
		return fresh, rErr
	}
	videoURLs := func() ([]string, error) {
		fresh, rErr := refresh()
		if rErr != nil {
			return nil, rErr
		}
		if fresh.Mode != plan.Mode || fresh.Video.Quality != plan.Video.Quality || fresh.Video.CodecID != plan.Video.CodecID {
			return nil, &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "续传地址画质变化"}
		}
		return fresh.Video.URLs, nil
	}
	audioURLs := func(orig *bilibili.SelectedAudioStream, fallback bool) func() ([]string, error) {
		return func() ([]string, error) {
			fresh, rErr := refresh()
			if rErr != nil {
				return nil, rErr
			}
			cand := fresh.Audio
			if fallback {
				cand = fresh.FallbackAudio
			}
			if orig == nil || cand == nil || cand.Quality != orig.Quality {
				return nil, &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "续传地址音质变化"}
			}
			return cand.URLs, nil
		}
	}

	var outFiles []library.MediaFile
	part := library.VideoPartRecord{
		CID:      page.CID,
		Page:     page.Page,
		Title:    page.Title,
		Duration: page.Duration,
		File:     mp4Rel,
		Video: library.PartVideoInfo{
			Quality:     plan.Video.Quality,
			QualityName: plan.Video.QualityName,
			CodecID:     plan.Video.CodecID,
			Codecs:      plan.Video.Codecs,
			Width:       plan.Video.Width,
			Height:      plan.Video.Height,
			FrameRate:   plan.Video.FrameRate,
		},
		Available: availableRecord(plan),
	}

	var summary MuxTrackSummary
	if plan.Mode == "dash" {
		vPath := filepath.Join(stageDir, fmt.Sprintf("%sP%02d.video-%d-%d.m4s", storage.TempPrefix, page.Page, plan.Video.Quality, plan.Video.CodecID))
		if _, _, err := m.downloadStreamWithRefresh(ctx, plan.Video.URLs, vPath, &subTask, videoURLs); err != nil {
			return library.VideoPartRecord{}, nil, err
		}
		var aPath string
		if plan.Audio != nil {
			part.Audio = library.PartAudioInfo{Quality: plan.Audio.Quality, Name: plan.Audio.Name, SavedAs: mp4Rel}
			mainAudio := plan.Audio
			isHigh := plan.Audio.Quality == 30250 || plan.Audio.Quality == 30251
			if isHigh {
				sideRel := fmt.Sprintf("P%02d.audio-%d.m4a", page.Page, plan.Audio.Quality)
				sideStaged, sErr := stagedFullPath(stageDir, sideRel)
				if sErr != nil {
					return library.VideoPartRecord{}, nil, sErr
				}
				n, hash, aErr := m.downloadStreamWithRefresh(ctx, plan.Audio.URLs, sideStaged, &subTask, audioURLs(plan.Audio, false))
				if aErr != nil {
					return library.VideoPartRecord{}, nil, aErr
				}
				part.Audio.SavedAs = sideRel
				outFiles = append(outFiles, library.MediaFile{Role: "audio", Path: sideRel, Bytes: n, SHA256: hash})
				mainAudio = plan.FallbackAudio
				if mainAudio == nil {
					return library.VideoPartRecord{}, nil, &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "缺少可合并普通音轨"}
				}
			} else if hasSameCID && !effectiveForceOverwrite(ctx, parentTask.Source, parentTask.ForceOverwrite) && localPart.Audio.SavedAs != "" && localPart.Audio.SavedAs != localPart.File && mediaIsValid(localMeta, localPart.Audio.SavedAs) {
				// Monotonic protection (Invariant #4): preserve verified local Hi-Res/Dolby sidecar audio!
				sideRel := fmt.Sprintf("P%02d.audio-%d.m4a", page.Page, localPart.Audio.Quality)
				var wantSHA string
				for _, mf := range localMeta.Media {
					if mf.Path == localPart.Audio.SavedAs {
						wantSHA = mf.SHA256
						break
					}
				}
				if sn, ssha, cErr := copyVerifiedFileToStageFromDstRel(localItemDir, localPart.Audio.SavedAs, wantSHA, stageDir, sideRel); cErr == nil {
					part.Audio = library.PartAudioInfo{Quality: localPart.Audio.Quality, Name: localPart.Audio.Name, SavedAs: sideRel}
					outFiles = append(outFiles, library.MediaFile{Role: "audio", Path: sideRel, Bytes: sn, SHA256: ssha})
				}
			}
			if mainAudio != nil {
				aPath = filepath.Join(stageDir, fmt.Sprintf("%sP%02d.audio-%d.m4s", storage.TempPrefix, page.Page, mainAudio.Quality))
				if _, _, err := m.downloadStreamWithRefresh(ctx, mainAudio.URLs, aPath, &subTask, audioURLs(mainAudio, isHigh)); err != nil {
					return library.VideoPartRecord{}, nil, err
				}
			}
		}
		muxTmp := storage.TempSiblingPath(mp4Staged)
		if _, err := MuxDASHFilesContext(ctx, vPath, aPath, muxTmp); err != nil {
			_ = os.Remove(muxTmp)
			return library.VideoPartRecord{}, nil, err
		}
		summary, err = InspectMP4FileContext(ctx, muxTmp)
		if err == nil {
			err = validateVideoOutput(summary, page.Duration, aPath != "")
		}
		if err != nil {
			_ = os.Remove(muxTmp)
			return library.VideoPartRecord{}, nil, err
		}
		if err := os.Rename(muxTmp, mp4Staged); err != nil {
			_ = os.Remove(muxTmp)
			return library.VideoPartRecord{}, nil, err
		}
		_ = os.Remove(vPath)
		if aPath != "" {
			_ = os.Remove(aPath)
		}
	} else {
		rawStage := mp4Staged + stageSuffix
		if _, _, err := m.downloadStreamWithRefresh(ctx, plan.Video.URLs, rawStage, &subTask, videoURLs); err != nil {
			return library.VideoPartRecord{}, nil, err
		}
		summary, err = EnsureSingleMP4Context(ctx, rawStage, float64(page.Duration))
		if err != nil {
			_ = os.Remove(rawStage)
			return library.VideoPartRecord{}, nil, err
		}
		if err := os.Rename(rawStage, mp4Staged); err != nil {
			_ = os.Remove(rawStage)
			return library.VideoPartRecord{}, nil, err
		}
	}

	outFiles = append(outFiles, library.MediaFile{
		Role:     "video",
		Path:     mp4Rel,
		Bytes:    summary.FileBytes,
		SHA256:   summary.SHA256,
		Width:    summary.Width,
		Height:   summary.Height,
		Duration: int(math.Round(summary.DurationSec)),
	})
	m.setFileProgress(parentTask, "video", mp4Rel, "completed", summary.FileBytes, summary.FileBytes, 0)

	forceOverwrite := effectiveForceOverwrite(ctx, parentTask.Source, parentTask.ForceOverwrite)
	reuseMonotonic := hasSameCID && !forceOverwrite

	// Supplements: preserve monotonic danmaku/subtitles when same CID (unless manual ForceOverwrite), otherwise fetch fresh.
	dmRel := fmt.Sprintf("P%02d.danmaku.xml", page.Page)
	if !cfg.SaveDanmaku {
		part.DanmakuState = "skipped"
	} else if reuseMonotonic && localPart.Danmaku != "" && mediaIsValid(localMeta, localPart.Danmaku) {
		var wantSHA string
		for _, mf := range localMeta.Media {
			if mf.Path == localPart.Danmaku {
				wantSHA = mf.SHA256
				break
			}
		}
		if dn, dsha, cErr := copyVerifiedFileToStageFromDstRel(localItemDir, localPart.Danmaku, wantSHA, stageDir, dmRel); cErr == nil {
			part.Danmaku = dmRel
			part.DanmakuState = "saved"
			outFiles = append(outFiles, library.MediaFile{Role: "danmaku", Path: dmRel, Bytes: dn, SHA256: dsha})
		}
	} else {
		if raw, dErr := m.client.DanmakuXML(ctx, page.CID, page.Duration); dErr == nil && len(raw) > 0 {
			if dn, dsha, wErr := writeBytesToStage(stageDir, dmRel, raw); wErr == nil {
				part.Danmaku = dmRel
				part.DanmakuState = "saved"
				outFiles = append(outFiles, library.MediaFile{Role: "danmaku", Path: dmRel, Bytes: dn, SHA256: dsha})
			}
		} else {
			part.DanmakuState = "missing"
		}
	}

	if reuseMonotonic && len(localPart.Chapters) > 0 {
		part.Chapters = append([]library.VideoChapter(nil), localPart.Chapters...)
	}
	if !cfg.SaveSubtitles {
		part.SubtitleState = "skipped"
		if len(part.Chapters) == 0 {
			extras, cErr := m.client.MissingSubtitlesAndChapters(ctx, bvid, page.CID, false, nil, false)
			if cErr != nil && (isStopError(cErr) || ctx.Err() != nil) {
				return library.VideoPartRecord{}, nil, cErr
			}
			if len(extras.Chapters) > 0 {
				part.Chapters = convertVideoChapters(extras.Chapters)
			}
		}
	} else {
		var savedLangs []string
		if reuseMonotonic {
			for _, tr := range localPart.SubtitleTracks {
				if mediaIsValid(localMeta, tr.VTT) && mediaIsValid(localMeta, tr.SRT) {
					lang := library.SanitizeFileName(tr.Language)
					if lang == "" {
						lang = "und"
					}
					vttDst := fmt.Sprintf("P%02d.%s.vtt", page.Page, lang)
					srtDst := fmt.Sprintf("P%02d.%s.srt", page.Page, lang)
					var vSHA, sSHA string
					for _, mf := range localMeta.Media {
						if mf.Path == tr.VTT {
							vSHA = mf.SHA256
						}
						if mf.Path == tr.SRT {
							sSHA = mf.SHA256
						}
					}
					vn, vsha, vErr := copyVerifiedFileToStageFromDstRel(localItemDir, tr.VTT, vSHA, stageDir, vttDst)
					sn, ssha, sErr := copyVerifiedFileToStageFromDstRel(localItemDir, tr.SRT, sSHA, stageDir, srtDst)
					if vErr == nil && sErr == nil {
						savedLangs = append(savedLangs, tr.Language)
						part.Subtitles = append(part.Subtitles, vttDst, srtDst)
						tr.VTT, tr.SRT = vttDst, srtDst
						part.SubtitleTracks = append(part.SubtitleTracks, tr)
						outFiles = append(outFiles,
							library.MediaFile{Role: "subtitle", Path: vttDst, Bytes: vn, SHA256: vsha},
							library.MediaFile{Role: "subtitle", Path: srtDst, Bytes: sn, SHA256: ssha},
						)
					}
				}
			}
		}
		extras, sErr := m.client.MissingSubtitlesAndChapters(ctx, bvid, page.CID, cfg.IncludeAI, savedLangs, true)
		if len(extras.Chapters) > 0 {
			part.Chapters = convertVideoChapters(extras.Chapters)
		}
		tracks := extras.Subtitles
		for _, tr := range tracks {
			if tr.ErrorKind != "" {
				continue
			}
			lang := library.SanitizeFileName(tr.Lan)
			if lang == "" {
				lang = "und"
			}
			vttDst := fmt.Sprintf("P%02d.%s.vtt", page.Page, lang)
			srtDst := fmt.Sprintf("P%02d.%s.srt", page.Page, lang)
			vn, vsha, vErr := writeBytesToStage(stageDir, vttDst, tr.VTT)
			sn, ssha, wErr := writeBytesToStage(stageDir, srtDst, tr.SRT)
			if vErr == nil && wErr == nil {
				part.Subtitles = append(part.Subtitles, vttDst, srtDst)
				part.SubtitleTracks = append(part.SubtitleTracks, library.PartSubtitleInfo{
					Language: tr.Lan, Name: tr.LanDoc, AI: tr.AI, VTT: vttDst, SRT: srtDst,
				})
				outFiles = append(outFiles,
					library.MediaFile{Role: "subtitle", Path: vttDst, Bytes: vn, SHA256: vsha},
					library.MediaFile{Role: "subtitle", Path: srtDst, Bytes: sn, SHA256: ssha},
				)
			}
		}
		if sErr == nil {
			part.SubtitleState = "saved"
			part.SubtitleIncludesAI = (reuseMonotonic && localPart.SubtitleIncludesAI) || cfg.IncludeAI
		} else {
			part.SubtitleState = "missing"
		}
	}

	return part, outFiles, nil
}

// --- Bilibili Dynamic Conflict Check ---

func cleanBilibiliImageIdentity(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if u, err := url.Parse(trimmed); err == nil && u.Path != "" {
		base := path.Base(u.Path)
		if tok := library.SanitizeMediaToken(strings.TrimSuffix(base, path.Ext(base))); tok != "" {
			return tok
		}
	}
	return ""
}

func bilibiliSourceKey(rawURL string) string {
	if clean := sanitizeStoredURL(rawURL); clean != "" {
		return clean
	}
	base, _, _ := strings.Cut(strings.TrimSpace(rawURL), "?")
	return base
}

func parseDynamicImageSlotIndex(relPath string, fallbackIdx int) int {
	base := filepath.Base(filepath.FromSlash(strings.TrimSpace(relPath)))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if n, err := strconv.Atoi(stem); err == nil && n > 0 {
		return n - 1
	}
	return fallbackIdx
}

func classifyBilibiliDynamicVsLocal(local library.ItemMetadata, dyn bilibili.DynamicItem, videoRefs []string, t *Task) (ConflictNotice, bool, error) {
	if !localHasProtectedContent(local) {
		return ConflictNotice{}, false, nil
	}
	if dyn.Degraded && local.Status == "complete" && len(local.Missing) == 0 {
		return ConflictNotice{}, false, wrapRemoteDegradedMsg("源站动态已降级或不可用，已保留本地原文件")
	}
	if localHasValidMedia(local) && len(dyn.Images) == 0 && len(videoRefs) == 0 {
		return ConflictNotice{}, false, wrapRemoteDegradedMsg("源站动态已无可用媒体，已保留本地原文件")
	}

	var localImages []library.MediaFile
	for _, mf := range local.Media {
		if mf.Role == "image" {
			localImages = append(localImages, mf)
		}
	}

	isPartial := local.Status != "complete" || len(local.Missing) > 0
	reasonKind := ""
	diffDesc := ""
	if len(dyn.Images) < len(localImages) || (!isPartial && len(localImages) != len(dyn.Images)) {
		reasonKind = ConflictReasonMedia
		diffDesc = fmt.Sprintf("配图数量变更（%d 张 -> %d 张）", len(localImages), len(dyn.Images))
	} else {
		for i, li := range localImages {
			slot := parseDynamicImageSlotIndex(li.Path, i)
			if slot < 0 || slot >= len(dyn.Images) {
				reasonKind = ConflictReasonMedia
				diffDesc = fmt.Sprintf("配图数量变更（%d 张 -> %d 张）", len(localImages), len(dyn.Images))
				break
			}
			lSrc := bilibiliSourceKey(li.Source)
			rSrc := bilibiliSourceKey(dyn.Images[slot].URL)
			if lSrc != "" && rSrc != "" && lSrc != rSrc {
				reasonKind = ConflictReasonMedia
				diffDesc = fmt.Sprintf("第 %d 张配图已替换", slot+1)
				break
			}
		}
	}

	if reasonKind == "" && !equalStringSlices(local.Videos, videoRefs) {
		reasonKind = ConflictReasonMedia
		diffDesc = "关联投稿视频列表已变更"
	}

	if reasonKind == "" {
		lText := normalizeCompareText(firstNonEmptyStr(local.Text, local.Title))
		rText := normalizeCompareText(firstNonEmptyStr(dyn.Text, dyn.Title))
		if lText != "" && rText != "" && lText != rText && !looksLikeTruncatedPrefix(lText, rText) && !looksLikeTruncatedPrefix(rText, lText) {
			reasonKind = ConflictReasonText
			diffDesc = "动态正文或标题已编辑"
		}
	}

	if reasonKind == "" {
		return ConflictNotice{}, false, nil
	}

	var imgURLs []string
	for _, img := range dyn.Images {
		imgURLs = append(imgURLs, img.URL)
	}
	fp := BilibiliDynamicConflictFingerprint(dyn.ID, imgURLs, videoRefs, dyn.Title, dyn.Text)
	return ConflictNotice{
		ItemKey:           library.CanonicalItemKey("bilibili", "dynamic", dyn.ID),
		Site:              "bilibili",
		Kind:              "dynamic",
		TargetID:          dyn.ID,
		AuthorUID:         strconv.FormatInt(dyn.Author.Mid, 10),
		AuthorName:        dyn.Author.Name,
		Title:             firstNonEmptyStr(dyn.Title, dyn.Text, local.Title, dyn.ID),
		Origin:            conflictOriginFromTask(t),
		ReasonKind:        reasonKind,
		LocalSummary:      fmt.Sprintf("已缓存 %d 张图、%d 个视频", len(localImages), len(local.Videos)),
		RemoteSummary:     fmt.Sprintf("源站当前 %d 张图、%d 个视频（%s）", len(dyn.Images), len(videoRefs), diffDesc),
		RemoteFingerprint: fp,
		Via:               local.Via,
	}, true, nil
}

// --- Douyin / Weibo / X WorkDownloadPlan Safeguards, Tripwire & Asset Helpers ---

func buildPlanAssetIDs(plan WorkDownloadPlan) []string {
	if len(plan.AssetIDs) > 0 {
		var out []string
		for _, raw := range plan.AssetIDs {
			if tok := library.SanitizeMediaToken(raw); tok != "" {
				out = append(out, tok)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	var derived []string
	if tok := library.SanitizeMediaToken(plan.MediaID); tok != "" {
		derived = append(derived, tok)
	}
	for _, p := range plan.Pages {
		if tok := library.SanitizeMediaToken(p.MediaID); tok != "" {
			derived = append(derived, tok)
		}
	}
	if plan.Article != nil {
		for _, blk := range plan.Article.Blocks {
			if tok := library.SanitizeMediaToken(blk.MediaID); tok != "" {
				derived = append(derived, tok)
			}
		}
	}
	return derived
}

func planHasAnyMediaURLs(plan WorkDownloadPlan) bool {
	if len(plan.OriginalURLs) > 0 || len(plan.VideoURLs) > 0 {
		return true
	}
	for _, p := range plan.Pages {
		if len(p.ImageURLs) > 0 || len(p.VideoURLs) > 0 {
			return true
		}
	}
	if plan.Article != nil {
		if len(plan.Article.CoverURLs) > 0 {
			return true
		}
		for _, blk := range plan.Article.Blocks {
			if len(blk.ImageURLs) > 0 {
				return true
			}
		}
	}
	return false
}

func normalizeComparablePageKind(siteID site.ID, rawKind string, isGIF bool) string {
	k := strings.ToLower(strings.TrimSpace(rawKind))
	switch k {
	case "live_photo", "motion":
		return "motion"
	case "animated_gif", "gif":
		if siteID == site.Weibo {
			return "image"
		}
		if siteID == site.X {
			return "video_gif"
		}
		return "gif"
	case "video":
		if isGIF && siteID == site.X {
			return "video_gif"
		}
		return "video"
	case "unknown":
		return "unknown"
	default:
		return "image"
	}
}

func planUpgradesSingleVideo(local library.ItemMetadata, plan WorkDownloadPlan) bool {
	if !mediaIsValid(local, "video.mp4") || local.DouyinVideo == nil {
		return true
	}
	dv := local.DouyinVideo
	if plan.Site == site.Douyin && len(plan.OriginalURLs) > 0 &&
		!strings.EqualFold(dv.Gear, douyinOriginalGear) &&
		!strings.EqualFold(dv.OriginalState, "unavailable") {
		return true
	}
	localPixels := dv.Width * dv.Height
	planPixels := plan.Width * plan.Height
	if localPixels > 0 && planPixels > localPixels {
		return true
	}
	if dv.Bitrate > 0 && plan.Bitrate > dv.Bitrate && (localPixels == 0 || planPixels >= localPixels) {
		return true
	}
	return false
}

func notePageVideoNeedsUpgrade(local *library.DouyinVideoInfo, remote PlannedNotePage) bool {
	if local == nil {
		return false
	}
	localPixels := local.Width * local.Height
	remotePixels := remote.Width * remote.Height
	if localPixels > 0 && remotePixels > localPixels {
		return true
	}
	if localPixels == 0 || remotePixels >= localPixels {
		if local.Bitrate > 0 && remote.Bitrate > local.Bitrate {
			return true
		}
		if local.FPS > 0 && remote.FPS > local.FPS {
			return true
		}
	}
	return false
}

func singleVideoIsDowngraded(local library.ItemMetadata, plan WorkDownloadPlan) bool {
	if !mediaIsValid(local, "video.mp4") || local.DouyinVideo == nil {
		return false
	}
	dv := local.DouyinVideo
	// Only enforce stream downgrade when referring to the same underlying video identity.
	lID := library.SanitizeMediaToken(dv.MediaID)
	if lID == "" && len(local.AssetIDs) == 1 {
		lID = library.SanitizeMediaToken(local.AssetIDs[0])
	}
	pID := library.SanitizeMediaToken(plan.MediaID)
	if lID != "" && pID != "" && lID != pID {
		return false
	}
	if durationChangedSubstantively(local.Duration, plan.DurationSec) {
		return false
	}
	if plan.Site == site.Douyin && strings.EqualFold(dv.Gear, douyinOriginalGear) &&
		len(plan.OriginalURLs) == 0 && !strings.EqualFold(plan.VideoGear, douyinOriginalGear) {
		return true
	}
	localPixels := dv.Width * dv.Height
	planPixels := plan.Width * plan.Height
	if localPixels > 0 && planPixels > 0 && planPixels < localPixels {
		return true
	}
	if plan.Site == site.X && dv.Bitrate > 0 && plan.Bitrate > 0 && plan.Bitrate < dv.Bitrate &&
		(planPixels == 0 || planPixels <= localPixels) {
		return true
	}
	return false
}

// checkWorkPlanDegradedVsLocal enforces Invariant #2 before any file download or
// swap starts, even when mode == ConflictResolveUseRemote or ConflictResolveKeepBoth.
func checkWorkPlanDegradedVsLocal(local library.ItemMetadata, plan WorkDownloadPlan) error {
	if !localHasProtectedContent(local) {
		return nil
	}
	if localHasValidMedia(local) && !planHasAnyMediaURLs(plan) {
		return wrapRemoteDegradedMsg("源站当前无可用媒体，已拒绝覆盖本地缓存")
	}
	if plan.Site == site.X {
		if len(plan.Pages) == 0 && isXPlanHLSOnly(plan) && mediaIsValid(local, "video.mp4") {
			return wrapRemoteDegradedMsg("源站当前仅剩 HLS 分段流，已保留本地 MP4")
		}
		for i, p := range plan.Pages {
			if (p.HLSOnly || isXPageHLSOnly(p)) && i < len(local.NotePages) {
				lp := local.NotePages[i]
				if lp.Video != "" && mediaIsValid(local, lp.Video) {
					return wrapRemoteDegradedMsg("源站媒体降级为 HLS 分段流，已保留本地 MP4")
				}
			}
		}
	}
	if len(plan.Pages) == 0 && len(local.NotePages) == 0 && singleVideoIsDowngraded(local, plan) {
		return wrapRemoteDegradedMsg("源站流清晰度低于本地已缓存版本，已保留本地原文件")
	}
	return nil
}

func extractLocalRootText(local library.ItemMetadata) string {
	txt := strings.TrimSpace(firstNonEmptyStr(local.Text, local.Desc))
	if local.Site == string(site.X) && len(local.Thread) > 0 {
		for _, seg := range local.Thread {
			if seg.ID == local.RawID && strings.TrimSpace(seg.Text) != "" {
				return strings.TrimSpace(seg.Text)
			}
		}
		if strings.TrimSpace(local.Desc) != "" {
			return strings.TrimSpace(local.Desc)
		}
	}
	return txt
}

func extractPlanFocalText(plan WorkDownloadPlan, rawID string) string {
	if plan.Site == site.X && len(plan.Thread) > 0 {
		for _, seg := range plan.Thread {
			if strings.TrimSpace(seg.ID) == strings.TrimSpace(rawID) && strings.TrimSpace(seg.Text) != "" {
				return strings.TrimSpace(seg.Text)
			}
		}
	}
	return firstNonEmptyStr(plan.Desc, plan.Title)
}

func extractPlanFocalShapeForFingerprint(plan WorkDownloadPlan, rawID string, planAssets []string) (remoteKind string, assets []string, pageCount int, singleVidDur int) {
	if plan.Site == site.X && len(plan.Thread) > 0 {
		for _, seg := range plan.Thread {
			if strings.TrimSpace(seg.ID) == strings.TrimSpace(rawID) {
				var focalPages []PlannedNotePage
				for _, idx := range seg.MediaIndexes {
					if idx >= 1 && idx <= len(plan.Pages) {
						focalPages = append(focalPages, plan.Pages[idx-1])
					}
				}
				for _, pg := range focalPages {
					if tok := library.SanitizeMediaToken(pg.MediaID); tok != "" {
						assets = append(assets, tok)
					}
				}
				if len(focalPages) == 1 && strings.EqualFold(focalPages[0].Kind, "video") {
					return "video", assets, 0, focalPages[0].DurationSec
				}
				return "dynamic", assets, len(focalPages), 0
			}
		}
	}
	planIsSingleVideo := len(plan.Pages) == 0 && (len(plan.VideoURLs) > 0 || len(plan.OriginalURLs) > 0 || plan.HLSOnly || strings.EqualFold(plan.Kind, "video"))
	if planIsSingleVideo {
		return "video", planAssets, 0, plan.DurationSec
	}
	return "dynamic", planAssets, len(plan.Pages), 0
}

func classifyWorkPlanVsLocal(local library.ItemMetadata, plan WorkDownloadPlan, itemKey, rawID string, t *Task) (ConflictNotice, bool, error) {
	if err := checkWorkPlanDegradedVsLocal(local, plan); err != nil {
		return ConflictNotice{}, false, err
	}
	if !localHasProtectedContent(local) {
		return ConflictNotice{}, false, nil
	}

	planAssets := buildPlanAssetIDs(plan)
	reasonKind := ""
	diffDesc := ""

	planIsSingleVideo := len(plan.Pages) == 0 && (len(plan.VideoURLs) > 0 || len(plan.OriginalURLs) > 0)
	localIsSingleVideo := len(local.NotePages) == 0 && ((local.DouyinVideo != nil && local.DouyinVideo.File != "") || local.Kind == "video")
	isXArticleUpgrade := isSafeXArticleUpgrade(local, plan)

	if local.Kind != "" && plan.Kind != "" && local.Kind != plan.Kind {
		reasonKind = ConflictReasonMedia
		diffDesc = fmt.Sprintf("作品类型变更（%s -> %s）", local.Kind, plan.Kind)
	} else if !isXArticleUpgrade && localIsSingleVideo != planIsSingleVideo && (localHasValidMedia(local) || planHasAnyMediaURLs(plan)) && !isSafeXThreadAppend(local, plan) {
		reasonKind = ConflictReasonMedia
		diffDesc = "媒体形态变更（视频与图文互换）"
	} else if planIsSingleVideo {
		lMediaID := ""
		if local.DouyinVideo != nil {
			lMediaID = library.SanitizeMediaToken(local.DouyinVideo.MediaID)
		}
		if lMediaID == "" && len(local.AssetIDs) > 0 {
			lMediaID = library.SanitizeMediaToken(local.AssetIDs[0])
		}
		pMediaID := library.SanitizeMediaToken(plan.MediaID)
		if pMediaID == "" && len(planAssets) > 0 {
			pMediaID = planAssets[0]
		}
		if lMediaID != "" && pMediaID != "" && lMediaID != pMediaID {
			reasonKind = ConflictReasonMedia
			diffDesc = "视频源文件已替换"
		} else if durationChangedSubstantively(local.Duration, plan.DurationSec) {
			reasonKind = ConflictReasonMedia
			diffDesc = fmt.Sprintf("视频时长变更（%ds -> %ds）", local.Duration, plan.DurationSec)
		}
	} else if !isXArticleUpgrade && (len(local.NotePages) > 0 || len(plan.Pages) > 0) {
		if len(local.NotePages) != len(plan.Pages) {
			// Check X self-thread pure append/ancestor prepend: if root + existing pages match and extra pages belong to thread replies/ancestors, allow safe enrichment!
			if !isSafeXThreadAppend(local, plan) {
				reasonKind = ConflictReasonMedia
				diffDesc = fmt.Sprintf("媒体数量变更（%d 项 -> %d 项）", len(local.NotePages), len(plan.Pages))
			}
		} else {
			if len(local.AssetIDs) > 0 && len(planAssets) > 0 && !equalStringSlices(local.AssetIDs, planAssets) {
				reasonKind = ConflictReasonMedia
				diffDesc = "媒体条目列表已替换或重排"
			}
			for i := 0; reasonKind == "" && i < len(plan.Pages); i++ {
				lp := local.NotePages[i]
				pp := plan.Pages[i]
				lID := library.SanitizeMediaToken(lp.MediaID)
				pID := library.SanitizeMediaToken(pp.MediaID)
				if lID != "" && pID != "" && lID != pID {
					reasonKind = ConflictReasonMedia
					diffDesc = fmt.Sprintf("第 %d 项媒体已替换", i+1)
					break
				}
				lKind := normalizeComparablePageKind(plan.Site, lp.Kind, lp.GIF)
				pKind := normalizeComparablePageKind(plan.Site, pp.Kind, strings.EqualFold(pp.Kind, "gif") || strings.EqualFold(pp.Kind, "animated_gif"))
				// Upgrading a still image to a LivePhoto motion pair on the same MediaID is safe enrichment.
				if lKind != pKind && !(lKind == "image" && pKind == "motion" && (lID == "" || pID == "" || lID == pID)) && lKind != "unknown" {
					reasonKind = ConflictReasonMedia
					diffDesc = fmt.Sprintf("第 %d 项媒体类型变更", i+1)
					break
				}
				if lID == "" && pID == "" && lKind == "video" && durationChangedSubstantively(lp.Duration, pp.DurationSec) {
					reasonKind = ConflictReasonMedia
					diffDesc = fmt.Sprintf("第 %d 项视频时长变更", i+1)
					break
				}
			}
		}
	}

	if reasonKind == "" && plan.Site == site.Douyin && local.Music != nil && plan.Music != nil {
		if local.Music.ID != "" && plan.Music.ID != "" && local.Music.ID != plan.Music.ID {
			reasonKind = ConflictReasonMedia
			diffDesc = "图集配乐已更换"
		}
	}

	// Check X Thread edits/deletions vs pure append or ThreadAncestors prepend (DEC-096).
	if reasonKind == "" && plan.Site == site.X && len(local.Thread) > 0 && len(plan.Thread) > 0 {
		if len(plan.Thread) < len(local.Thread) {
			reasonKind = ConflictReasonText
			diffDesc = "作者自回复串帖已删减"
		} else if _, textErr, ok := findXThreadSubsequenceStart(local.Thread, plan.Thread); !ok {
			reasonKind = ConflictReasonText
			if textErr {
				diffDesc = "作者自回复串帖正文已编辑"
			} else {
				diffDesc = "作者自回复串帖结构已变更"
			}
		}
	}

	// Substantive text edit check (respecting Weibo LongText expansion, X Article upgrade & monotonic truncation guard).
	// Basic site.DownloadPlan callers leave Desc empty and only supply a display Title; compare body text
	// only when plan.Desc is present or remote edit metadata advanced.
	if reasonKind == "" && !isXArticleUpgrade {
		editAdvanced := (plan.EditCount > 0 && plan.EditCount > local.EditCount) ||
			(plan.EditTs > 0 && local.EditTs > 0 && plan.EditTs > local.EditTs)
		if strings.TrimSpace(plan.Desc) != "" || editAdvanced {
			localTxt := normalizeCompareText(extractLocalRootText(local))
			remoteTxt := normalizeCompareText(extractPlanFocalText(plan, rawID))
			if localTxt != "" && remoteTxt != "" && localTxt != remoteTxt {
				isLongTextExpansion := !editAdvanced && (looksLikeTruncatedPrefix(localTxt, remoteTxt) || looksLikeTruncatedPrefix(remoteTxt, localTxt))
				if !isLongTextExpansion {
					reasonKind = ConflictReasonText
					diffDesc = "正文内容已编辑"
				}
			} else if editAdvanced && localTxt != remoteTxt {
				reasonKind = ConflictReasonText
				diffDesc = "源站记录了新的编辑版本"
			}
		}
	}

	if reasonKind == "" {
		return ConflictNotice{}, false, nil
	}

	localMediaDesc := describeLocalWorkSummary(local)
	remoteMediaDesc := describeRemotePlanSummary(plan, diffDesc)
	fpKind, fpAssets, fpPages, fpVidDur := extractPlanFocalShapeForFingerprint(plan, rawID, planAssets)
	fp := WorkConflictFingerprint(
		string(plan.Site),
		fpKind,
		rawID,
		fpAssets,
		plan.EditCount,
		fpPages,
		fpVidDur,
		extractPlanFocalText(plan, rawID),
	)

	return ConflictNotice{
		ItemKey:           itemKey,
		Site:              string(plan.Site),
		Kind:              plan.Kind,
		TargetID:          rawID,
		AuthorUID:         plan.AuthorUID,
		AuthorName:        firstNonEmptyStr(plan.AuthorName, local.Author.Name),
		Title:             firstNonEmptyStr(plan.Title, plan.Desc, local.Title, rawID),
		Origin:            conflictOriginFromTask(t),
		ReasonKind:        reasonKind,
		LocalSummary:      localMediaDesc,
		RemoteSummary:     remoteMediaDesc,
		RemoteFingerprint: fp,
		Via:               local.Via,
	}, true, nil
}

// isSafeXArticleUpgrade reports whether plan non-destructively upgrades an older
// X article post cached only as a degraded CardKindArticle / URL stub into a
// full offline ItemArticle (DEC-093 / DEC-096).
func isSafeXArticleUpgrade(local library.ItemMetadata, plan WorkDownloadPlan) bool {
	if plan.Site != site.X || local.Article != nil || plan.Article == nil {
		return false
	}
	if local.Card != nil && strings.EqualFold(strings.TrimSpace(local.Card.Kind), "article") {
		return true
	}
	return isDegradedXArticleStubText(extractLocalRootText(local))
}

func isDegradedXArticleStubText(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return true
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return true
	}
	for _, f := range fields {
		lower := strings.ToLower(f)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			return false
		}
	}
	return true
}

// findXThreadSubsequenceStart finds the contiguous slice in planThread that
// matches localThread (supporting both trailing self-replies and prepended
// ThreadAncestors, DEC-096).
func findXThreadSubsequenceStart(localThread []library.ItemThreadPart, planThread []PlannedThreadPart) (startIdx int, textMismatch bool, ok bool) {
	if len(localThread) == 0 || len(planThread) < len(localThread) {
		return -1, false, false
	}
	firstID := strings.TrimSpace(localThread[0].ID)
	maxStart := len(planThread) - len(localThread)
	tryAt := func(offset int) (bool, bool) {
		for i := range localThread {
			lID := strings.TrimSpace(localThread[i].ID)
			pID := strings.TrimSpace(planThread[offset+i].ID)
			if lID != "" && pID != "" && lID != pID {
				return false, false
			}
			lt := normalizeCompareText(localThread[i].Text)
			pt := normalizeCompareText(planThread[offset+i].Text)
			if lt != "" && pt != "" && lt != pt && !looksLikeTruncatedPrefix(lt, pt) && !looksLikeTruncatedPrefix(pt, lt) {
				return false, true
			}
		}
		return true, false
	}
	if firstID != "" {
		for offset := 0; offset <= maxStart; offset++ {
			if strings.TrimSpace(planThread[offset].ID) == firstID {
				matched, txtErr := tryAt(offset)
				return offset, txtErr, matched
			}
		}
		return -1, false, false
	}
	matched, txtErr := tryAt(0)
	if matched {
		return 0, false, true
	}
	return -1, txtErr, false
}

func isSafeXThreadAppend(local library.ItemMetadata, plan WorkDownloadPlan) bool {
	if plan.Site != site.X || len(plan.Thread) <= len(local.Thread) {
		return false
	}
	startIdx := 0
	if len(local.Thread) > 0 {
		idx, _, ok := findXThreadSubsequenceStart(local.Thread, plan.Thread)
		if !ok {
			return false
		}
		startIdx = idx
	}

	// Case 1: Single-video focal tweet expanding into a multi-page self-reply thread (DEC-096).
	localIsSingleVideo := len(local.NotePages) == 0 && local.DouyinVideo != nil && local.DouyinVideo.File != ""
	if localIsSingleVideo {
		if len(plan.Pages) < 2 {
			return false
		}
		lMediaID := library.SanitizeMediaToken(local.DouyinVideo.MediaID)
		if lMediaID == "" && len(local.AssetIDs) > 0 {
			lMediaID = library.SanitizeMediaToken(local.AssetIDs[0])
		}
		var candIndexes []int
		if len(local.Thread) > 0 {
			for i := 0; i < len(local.Thread); i++ {
				candIndexes = append(candIndexes, plan.Thread[startIdx+i].MediaIndexes...)
			}
		} else {
			for _, seg := range plan.Thread {
				if strings.TrimSpace(seg.ID) != "" && strings.TrimSpace(seg.ID) == strings.TrimSpace(local.RawID) {
					candIndexes = append(candIndexes, seg.MediaIndexes...)
					break
				}
			}
		}
		if len(candIndexes) == 0 {
			for i := range plan.Pages {
				candIndexes = append(candIndexes, i+1)
			}
		}
		for _, oneBased := range candIndexes {
			if oneBased < 1 || oneBased > len(plan.Pages) {
				continue
			}
			pp := plan.Pages[oneBased-1]
			pKind := normalizeComparablePageKind(site.X, pp.Kind, strings.EqualFold(pp.Kind, "gif") || strings.EqualFold(pp.Kind, "animated_gif"))
			if pKind != "video" && pKind != "video_gif" {
				continue
			}
			pID := library.SanitizeMediaToken(pp.MediaID)
			if lMediaID != "" && pID != "" {
				if lMediaID == pID {
					return true
				}
				continue
			}
			if !durationChangedSubstantively(local.Duration, pp.DurationSec) {
				return true
			}
		}
		return false
	}

	if len(plan.Pages) <= len(local.NotePages) {
		return false
	}

	// Case 2: Multi-page or text-only focal tweet gaining thread replies and/or prepended ThreadAncestors.
	// Align local.NotePages to plan.Pages via MediaIndexes when ThreadAncestors shifted indices.
	var alignedPlanPages []PlannedNotePage
	if len(local.NotePages) > 0 {
		var mappedIdx []int
		if len(local.Thread) > 0 {
			for i := 0; i < len(local.Thread); i++ {
				mappedIdx = append(mappedIdx, plan.Thread[startIdx+i].MediaIndexes...)
			}
		} else if rawID := strings.TrimSpace(local.RawID); rawID != "" {
			for _, seg := range plan.Thread {
				if strings.TrimSpace(seg.ID) == rawID {
					mappedIdx = append(mappedIdx, seg.MediaIndexes...)
					break
				}
			}
		}
		if len(mappedIdx) == len(local.NotePages) {
			valid := true
			for _, oneBased := range mappedIdx {
				if oneBased < 1 || oneBased > len(plan.Pages) {
					valid = false
					break
				}
				alignedPlanPages = append(alignedPlanPages, plan.Pages[oneBased-1])
			}
			if !valid {
				alignedPlanPages = nil
			}
		}
		if len(alignedPlanPages) != len(local.NotePages) {
			alignedPlanPages = plan.Pages[:len(local.NotePages)]
		}
	}

	for i := range local.NotePages {
		lp := local.NotePages[i]
		pp := alignedPlanPages[i]
		lID := library.SanitizeMediaToken(lp.MediaID)
		pID := library.SanitizeMediaToken(pp.MediaID)
		if lID != "" && pID != "" && lID != pID {
			return false
		}
		lKind := normalizeComparablePageKind(site.X, lp.Kind, lp.GIF)
		pKind := normalizeComparablePageKind(site.X, pp.Kind, strings.EqualFold(pp.Kind, "gif") || strings.EqualFold(pp.Kind, "animated_gif"))
		if lKind != pKind && !(lKind == "image" && pKind == "motion" && (lID == "" || pID == "" || lID == pID)) && lKind != "unknown" {
			return false
		}
	}
	return true
}

func describeLocalWorkSummary(local library.ItemMetadata) string {
	if len(local.NotePages) > 0 {
		return fmt.Sprintf("已缓存 %d 项媒体（%s）", len(local.NotePages), firstNonEmptyStr(local.Title, local.Text, local.RawID))
	}
	if local.DouyinVideo != nil && local.DouyinVideo.File != "" {
		q := firstNonEmptyStr(local.DouyinVideo.QualityName, local.DouyinVideo.Gear, "视频")
		return fmt.Sprintf("已缓存视频 %s（%ds）", q, local.Duration)
	}
	return fmt.Sprintf("已缓存正文（%s）", firstNonEmptyStr(local.Title, local.Text, local.RawID))
}

func describeRemotePlanSummary(plan WorkDownloadPlan, diffDesc string) string {
	if len(plan.Pages) > 0 {
		return fmt.Sprintf("源站当前 %d 项媒体（%s）", len(plan.Pages), diffDesc)
	}
	if len(plan.VideoURLs) > 0 || len(plan.OriginalURLs) > 0 {
		q := firstNonEmptyStr(plan.QualityName, plan.VideoGear, "视频")
		return fmt.Sprintf("源站当前视频 %s（%s）", q, diffDesc)
	}
	return fmt.Sprintf("源站当前正文（%s）", diffDesc)
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// findReusableLocalPage looks up an unchanged verified page from localMeta whose
// MediaID matches pageMediaID (or, during safe same-source completion/upgrade
// when mode == "", at matching page index) so verified local files can be copied
// into stageDir without data loss when remote CDN links expire.
func findReusableLocalPage(localMeta library.ItemMetadata, page PlannedNotePage, pageIdx int, isConflictReplace bool) (library.NotePageRecord, bool) {
	pID := library.SanitizeMediaToken(page.MediaID)
	if pID != "" {
		for _, lp := range localMeta.NotePages {
			if library.SanitizeMediaToken(lp.MediaID) == pID {
				return lp, true
			}
		}
	}
	if !isConflictReplace {
		for _, lp := range localMeta.NotePages {
			if lp.Index == pageIdx && lp.MediaID == "" {
				return lp, true
			}
		}
	}
	return library.NotePageRecord{}, false
}

func copyLocalMediaToStageAs(localMeta library.ItemMetadata, srcRel, dstStem, role, stageDir string) (*library.MediaFile, error) {
	if srcRel == "" || !mediaIsValid(localMeta, srcRel) {
		return nil, fmt.Errorf("local media %q not valid", srcRel)
	}
	var srcMF library.MediaFile
	found := false
	for _, mf := range localMeta.Media {
		if mf.Path == srcRel {
			srcMF = mf
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("local media %q not in manifest", srcRel)
	}
	ext := filepath.Ext(srcRel)
	if ext == "" {
		ext = ".jpg"
	}
	dstRel := dstStem
	if filepath.Ext(dstStem) == "" {
		dstRel = dstStem + ext
	}
	n, sha, err := copyVerifiedFileToStageFromDstRel(localMeta.ItemDir, srcRel, srcMF.SHA256, stageDir, dstRel)
	if err != nil {
		return nil, err
	}
	out := srcMF
	if role != "" {
		out.Role = role
	}
	out.Path = dstRel
	out.Bytes = n
	out.SHA256 = sha
	return &out, nil
}

func cloneLibraryOriginDepth(src *library.ItemOrigin, depth int) *library.ItemOrigin {
	if src == nil {
		return nil
	}
	cp := *src
	cp.Links = append([]library.ItemLink(nil), cp.Links...)
	if cp.Card != nil {
		cc := *cp.Card
		cp.Card = &cc
	}
	if cp.InReplyTo != nil {
		ir := *cp.InReplyTo
		cp.InReplyTo = &ir
	}
	if depth >= 1 {
		cp.Origin = nil
	} else {
		cp.Origin = cloneLibraryOriginDepth(cp.Origin, depth+1)
	}
	return &cp
}

func isValidLocalOriginSnapshot(orig *library.ItemOrigin) bool {
	return orig != nil && !orig.Unavailable &&
		(strings.TrimSpace(orig.Text) != "" ||
			strings.TrimSpace(orig.Author.Name) != "" ||
			strings.TrimSpace(orig.ID) != "")
}

// preserveValidOriginSnapshot enforces DEC-076 monotonic protection on an Origin
// and its depth-1 nested Origin so remote unavailable placeholders or empty
// payloads never overwrite a valid locally archived snapshot during safe upgrades
// or non-forced re-caching. The returned bool is true only when the top-level
// origin itself was restored from prev.
func preserveValidOriginSnapshot(candidate, prev *library.ItemOrigin, isConflictReplace, forceOverwrite bool, depth int) (*library.ItemOrigin, bool) {
	if forceOverwrite {
		return candidate, false
	}
	if isValidLocalOriginSnapshot(prev) && (candidate == nil || candidate.Unavailable) {
		sameOrigin := candidate == nil || candidate.ID == "" || prev.ID == "" || candidate.ID == prev.ID
		if !isConflictReplace || (candidate != nil && candidate.Unavailable && sameOrigin) {
			return cloneLibraryOriginDepth(prev, depth), true
		}
	}
	if depth == 0 && candidate != nil && !candidate.Unavailable && prev != nil && prev.Origin != nil {
		sameOuter := candidate.ID == "" || prev.ID == "" || candidate.ID == prev.ID
		if sameOuter {
			nested, nestedPreserved := preserveValidOriginSnapshot(candidate.Origin, prev.Origin, isConflictReplace, forceOverwrite, depth+1)
			if nestedPreserved {
				cp := cloneLibraryOriginDepth(candidate, 0)
				cp.Origin = nested
				return cp, false
			}
		}
	}
	return candidate, false
}

// preserveValidLocalOrigin ensures a valid local Origin (and its fromOrigin media)
// is never overwritten by Origin.Unavailable == true or nil during safe upgrades.
func preserveValidLocalOrigin(candidate *library.ItemOrigin, previous library.ItemMetadata, isConflictReplace bool) (*library.ItemOrigin, bool) {
	return preserveValidOriginSnapshot(candidate, previous.Origin, isConflictReplace, false, 0)
}
