package ui

import (
	"bytes"
	"context"
	"image"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

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
	bmp, exists := a.imageCache[key]
	if exists {
		a.mu.Unlock()
		return bmp
	}
	if retry, ok := a.imageRetry[key]; ok && (retry.tries >= remoteImageTries || time.Now().Before(retry.at)) {
		a.mu.Unlock()
		return nil
	}
	if a.dispatch == nil || a.CurrentRoute == RouteHome || (a.CurrentRoute == RouteVideo && a.CurrentLibraryDetail != nil) {
		a.mu.Unlock()
		return nil
	}
	if len(a.imageKeys) >= 128 {
		delete(a.imageCache, a.imageKeys[0])
		a.imageKeys = a.imageKeys[1:]
	}
	a.imageKeys = append(a.imageKeys, key)
	a.imageCache[key] = nil
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
		if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 24_000_000 {
			return nil, err
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
		if _, ok := a.imageCache[key]; ok {
			a.imageCache[key] = bmp
		}
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
		if bmp, ok := a.imageCache[key]; ok && bmp == nil {
			delete(a.imageCache, key)
			for i, k := range a.imageKeys {
				if k == key {
					a.imageKeys = append(a.imageKeys[:i], a.imageKeys[i+1:]...)
					break
				}
			}
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

func (a *App) faceURL(mid int64) string {
	if mid <= 0 {
		return ""
	}
	if account := a.backend.AccountStatus(); account.Mid == mid {
		return account.Face
	}
	if a.CurrentUser != nil && a.CurrentUser.Mid == mid {
		return a.CurrentUser.Face
	}
	if a.CurrentVideo != nil && a.CurrentVideo.Author.Mid == mid {
		return a.CurrentVideo.Author.Face
	}
	for _, sub := range a.Subscriptions {
		for _, acc := range sub.Accounts {
			if acc.UID == strconv.FormatInt(mid, 10) {
				return acc.Face
			}
		}
		if sub.Mid == mid {
			return sub.Face
		}
	}
	for _, user := range a.ImportFollowings {
		if user.Mid == mid {
			return user.Face
		}
	}
	for _, entry := range a.FeedItems {
		if entry.Mid == mid {
			return entry.AuthorFace
		}
	}
	return ""
}

func firstImageURL(urls []string) string {
	for _, url := range urls {
		if strings.TrimSpace(url) != "" {
			return url
		}
	}
	return ""
}

// loadThumbBitmap decodes an image file scaled to fit maxEdge.
func loadThumbBitmap(path string, maxEdge int) *ui.Bitmap {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
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
