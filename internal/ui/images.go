package ui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// maxImageReadBytes caps local image reads before os.ReadFile so a mis-referenced
// multi-GB media file (e.g. a coverless video) is never loaded into memory.
const maxImageReadBytes = 32 << 20

// Thumbnail requests use public, cookie-free image fetching. A failed fetch is
// retried after a pause (a few times at most), so one network hiccup does not
// leave a post on its placeholder for the rest of the session; rendering never
// retries every frame. Tests without a dispatcher use only their explicitly
// supplied local images.
func (a *App) lookupRemoteBitmap(rawURL string) *ui.Bitmap {
	if rawURL == "" {
		return nil
	}
	key := "public-image:" + rawURL
	a.mu.Lock()
	bmp, exists := a.images.get(key)
	if exists {
		a.mu.Unlock()
		return bmp
	}
	if retry, ok := a.imageRetry[key]; ok && (retry.tries >= remoteImageTries || time.Now().Before(retry.at)) {
		a.mu.Unlock()
		return nil
	}
	if a.dispatch == nil || a.CurrentRoute == RouteHome || a.CurrentRoute == RouteLibrary || a.CurrentRoute == RouteLibraryItem || (a.CurrentRoute == RouteSubscriptions && a.SubDetailOpen && normalizeSubDetailTab(a.SubDetailTab) == "content") || (a.CurrentRoute == RouteVideo && a.CurrentLibraryDetail != nil) || (a.CurrentRoute == RouteDouyinWork && a.CurrentLibraryDetail != nil) {
		a.mu.Unlock()
		return nil
	}
	a.images.reserve(key)
	a.mu.Unlock()
	requestGuarded(a, key, nil, func(ctx context.Context) (*ui.Bitmap, error) {
		select {
		case a.imageQueue <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-a.imageQueue }()
		raw, err := a.backend.FetchImage(ctx, rawURL)
		if err != nil {
			return nil, err
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 24_000_000 {
			return nil, errors.New("image dimensions out of range")
		}
		img, _, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		w, h := config.Width, config.Height
		if w > 640 {
			h, w = max(1, h*640/w), 640
		}
		if h > 640 {
			w, h = max(1, w*640/h), 640
		}
		small := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(small, small.Bounds(), img, img.Bounds(), draw.Src, nil)
		return ui.NewBitmap(small), nil
	}, func(bmp *ui.Bitmap) {
		a.mu.Lock()
		a.images.fill(key, bmp)
		delete(a.imageRetry, key)
		a.mu.Unlock()
	}, func(error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.imageRetry == nil {
			a.imageRetry = make(map[string]imageRetry)
		}
		r := a.imageRetry[key]
		r.tries++
		r.at = time.Now().Add(time.Duration(r.tries) * remoteImageRetryPause)
		a.imageRetry[key] = r
		// Free the slot so the next lookup after the pause fetches again.
		if bmp, ok := a.images.get(key); ok && bmp == nil {
			a.images.remove(key)
		}
	})
	return nil
}

// imageRetry tracks failed remote thumbnails: how often they failed and when
// the next attempt may start.
type imageRetry struct {
	tries int
	at    time.Time
}

const (
	remoteImageTries      = 3
	remoteImageRetryPause = 20 * time.Second
)

type faceIndexSig struct {
	subsRev    int
	subsN      int
	followN    int
	liveAcctsN int
	feedN      int
}

func (a *App) ensureFaceIndex() {
	sig := faceIndexSig{
		subsRev:    a.subsRev,
		subsN:      len(a.Subscriptions),
		followN:    len(a.ImportFollowings),
		liveAcctsN: len(a.LiveAccounts),
		feedN:      len(a.FeedItems),
	}
	if a.faceIndexValid && a.faceIndexSig == sig {
		return
	}
	a.faceIndexSig = sig
	a.faceIndexValid = true
	bili := make(map[int64]string, len(a.Subscriptions)+len(a.ImportFollowings))
	acct := make(map[string]string, len(a.Subscriptions)+len(a.LiveAccounts))

	for _, entry := range a.FeedItems {
		if entry.AuthorFace == "" {
			continue
		}
		if entry.Mid > 0 && bili[entry.Mid] == "" {
			bili[entry.Mid] = entry.AuthorFace
		}
		if entry.AuthorUID != "" {
			k := strings.ToLower(strings.TrimSpace(entry.Site)) + ":" + entry.AuthorUID
			if acct[k] == "" {
				acct[k] = entry.AuthorFace
			}
		}
	}
	for _, st := range a.LiveAccounts {
		if st.Face != "" && st.UID != "" {
			acct[strings.ToLower(strings.TrimSpace(st.Site))+":"+st.UID] = st.Face
		}
	}
	for _, user := range a.ImportFollowings {
		if user.Mid > 0 && user.Face != "" {
			bili[user.Mid] = user.Face
		}
	}
	for _, sub := range a.Subscriptions {
		if sub.Mid > 0 && sub.Face != "" {
			bili[sub.Mid] = sub.Face
		}
		for _, acc := range sub.Accounts {
			if acc.Face == "" || acc.UID == "" {
				continue
			}
			s := normalizeSiteIDUI(acc.Site)
			acct[s+":"+acc.UID] = acc.Face
			if s == "bilibili" {
				if mid, err := strconv.ParseInt(acc.UID, 10, 64); err == nil && mid > 0 {
					bili[mid] = acc.Face
				}
			}
		}
	}
	a.biliFaceIndex = bili
	a.acctFaceIndex = acct
}

func (a *App) faceURL(mid int64) string {
	if mid <= 0 {
		return ""
	}
	if account := a.backend.AccountStatus(); account.Mid == mid && account.Face != "" {
		return account.Face
	}
	if a.CurrentUser != nil && a.CurrentUser.Mid == mid && a.CurrentUser.Face != "" {
		return a.CurrentUser.Face
	}
	if a.CurrentVideo != nil && a.CurrentVideo.Author.Mid == mid && a.CurrentVideo.Author.Face != "" {
		return a.CurrentVideo.Author.Face
	}
	a.ensureFaceIndex()
	return a.biliFaceIndex[mid]
}

func (a *App) faceURLForAccount(siteID, uid string) string {
	siteID = strings.ToLower(strings.TrimSpace(siteID))
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return ""
	}
	if siteID == "" || siteID == "bilibili" {
		if mid, err := strconv.ParseInt(uid, 10, 64); err == nil && mid > 0 {
			return a.faceURL(mid)
		}
		return ""
	}
	if st := a.backend.StatusOf(siteID); st.SecUID == uid || st.UID == uid {
		if st.Face != "" {
			return st.Face
		}
	}
	if a.CurrentDouyinUser != nil && (a.CurrentDouyinUser.SecUID == uid || a.CurrentDouyinUser.UID == uid) && a.CurrentDouyinUser.AvatarURL != "" {
		return a.CurrentDouyinUser.AvatarURL
	}
	if a.CurrentDouyinWork != nil && (a.CurrentDouyinWork.Author.SecUID == uid || a.CurrentDouyinWork.Author.UID == uid) && a.CurrentDouyinWork.Author.AvatarURL != "" {
		return a.CurrentDouyinWork.Author.AvatarURL
	}
	a.ensureFaceIndex()
	return a.acctFaceIndex[siteID+":"+uid]
}

func firstImageURL(urls []string) string {
	for _, url := range urls {
		if strings.TrimSpace(url) != "" {
			return url
		}
	}
	return ""
}

func readBoundedImageFile(path string) []byte {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxImageReadBytes {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return nil
	}
	return raw
}

// loadThumbBitmap decodes an image file scaled to fit maxEdge.
func loadThumbBitmap(path string, maxEdge int) *ui.Bitmap {
	raw := readBoundedImageFile(path)
	if len(raw) == 0 {
		return nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil || img == nil {
		return nil
	}
	w, h := cfg.Width, cfg.Height
	if w > maxEdge {
		h, w = max(1, h*maxEdge/w), maxEdge
	}
	if h > maxEdge {
		w, h = max(1, w*maxEdge/h), maxEdge
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == cfg.Width && h == cfg.Height {
		draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Src)
	} else {
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	}
	return ui.NewBitmap(dst)
}
