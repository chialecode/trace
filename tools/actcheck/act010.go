package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/sitecheck"
)

const (
	act010ID                    = "act010"
	act010Label                 = "ACT-010"
	act010MaxRuns               = 1
	act010MaxAPICalls           = 18
	act010MaxVODRequests        = 16
	act010MaxVODBytes     int64 = 64 << 20 // 64 MiB
	act010LargeFileCutoff int64 = 24 << 20 // 24 MiB: probe head 8 MiB + tail 1 MiB only
	act010RangeChunkBytes int64 = 8 << 20  // 8 MiB closed Range chunks
	act010TailProbeBytes  int64 = 1 << 20  // 1 MiB tail Range chunk
	act010MaxLiveConns          = 2
	act010MaxLiveBytes    int64 = 50 << 20 // 50 MiB
	act010MaxRecord             = 25 * time.Second
	act010DefaultRecord         = 20 * time.Second
	act010DefaultSpacing        = 1200 * time.Millisecond
)

var act010APILimits = map[string]int{
	"profile_self":  2,
	"profile_other": 2,
	"aweme_post":    3,
	"aweme_detail":  4,
	"web_enter":     4,
	"reflow_info":   2,
	"hls_playlist":  1,
}

var (
	reAct010SecUID    = regexp.MustCompile(`^MS4wLjAB[A-Za-z0-9_-]{16,}$`)
	reAct010WorkID    = regexp.MustCompile(`^[1-9][0-9]{14,21}$`)
	reAct010RoomDigit = regexp.MustCompile(`^[1-9][0-9]{2,21}$`)
	reAct010AllDigits = regexp.MustCompile(`^[0-9]+$`)
)

type act010Targets struct {
	Version    int              `json:"version"`
	Act        string           `json:"act"`
	ProvidedAt string           `json:"providedAt"`
	Users      []act010User     `json:"users"`
	Works      []act010Work     `json:"works"`
	LiveRooms  []act010LiveRoom `json:"liveRooms"`
}

type act010User struct {
	SecUID string `json:"secUid"`
	URL    string `json:"url"`
}

type act010Work struct {
	AwemeID string   `json:"awemeId"`
	Kind    string   `json:"kind"`   // "video" | "note"
	Expect  []string `json:"expect"` // "pinned" | "recent" | "mixed"
	URL     string   `json:"url"`
}

type act010LiveRoom struct {
	WebRID string `json:"webRid"`
	Form   string `json:"form"` // "short_id" | "other"
	URL    string `json:"url"`
}

type act010Config struct {
	RootDir        string
	OutPath        string
	RecordDuration time.Duration
	MinSpacing     time.Duration // 0 => 1.2s default; <0 => 0s in synthetic tests
	Endpoints      douyin.Endpoints
	Transport      http.RoundTripper
	AllowLocalhost bool
}

type act010Receipt struct {
	Act              string                 `json:"act"`
	Commit           string                 `json:"commit,omitempty"`
	StartedAt        string                 `json:"startedAt"`
	FinishedAt       string                 `json:"finishedAt"`
	Passed           bool                   `json:"passed"`
	StoppedBy        string                 `json:"stoppedBy,omitempty"`
	TempMediaDeleted bool                   `json:"tempMediaDeleted"`
	SecretScanClean  bool                   `json:"secretScanClean"`
	Steps            []step                 `json:"steps"`
	Verifications    map[string]any         `json:"verifications"`
	APIUsage         sitecheck.Usage        `json:"apiUsage"`
	APIStatusCodes   map[string]map[int]int `json:"apiStatusCodes"`
	VODMedia         mediaUsage             `json:"vodMedia"`
	LiveMedia        mediaUsage             `json:"liveMedia"`
	StatusCounts     map[string]int         `json:"httpStatusCounts"`
	Redirects        []map[string]any       `json:"redirects,omitempty"`
	Limits           map[string]any         `json:"limits"`
	Notes            []string               `json:"notes,omitempty"`
}

func runAct010Main(root, out string, recordFor time.Duration) {
	rc, err := runAct010(context.Background(), act010Config{
		RootDir:        root,
		OutPath:        out,
		RecordDuration: recordFor,
	})
	if err != nil && rc.StartedAt == "" {
		fatal(err.Error())
	}
	outDisp := out
	if outDisp == "" {
		outDisp = filepath.Join(root, "artifacts", "site-check", "act010.json")
	}
	fmt.Printf("%s passed=%v stoppedBy=%q api=%d vodMedia=%d/%dB liveMedia=%d/%dB receipt=%s\n",
		act010Label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls,
		rc.VODMedia.Requests, rc.VODMedia.Bytes, rc.LiveMedia.Requests, rc.LiveMedia.Bytes, outDisp)
	for _, s := range rc.Steps {
		fmt.Printf("  %-26s ok=%v skipped=%v %s %s\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Error)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ACT-010 error: %v\n", err)
	}
	if err != nil || !rc.Passed {
		os.Exit(1)
	}
}

func runAct010(parentCtx context.Context, cfg act010Config) (act010Receipt, error) {
	var rc act010Receipt
	root := cfg.RootDir
	if root == "" {
		root = "."
	}
	recordFor := cfg.RecordDuration
	if recordFor == 0 {
		recordFor = act010DefaultRecord
	}
	if recordFor < 0 || recordFor > act010MaxRecord {
		return rc, fmt.Errorf("recording duration %s exceeds authorized %s limit for ACT-010", recordFor, act010MaxRecord)
	}
	outPath := cfg.OutPath
	if outPath == "" {
		outPath = filepath.Join(root, "artifacts", "site-check", "act010.json")
	}
	spacing := cfg.MinSpacing
	if spacing == 0 {
		spacing = act010DefaultSpacing
	} else if spacing < 0 {
		spacing = 0
	}

	tg, err := loadAndValidateAct010Targets(root)
	if err != nil {
		return rc, err
	}

	rawCookie, cleanedInitial, err := readAndValidateDouyinCookie(root)
	if err != nil {
		return rc, err
	}

	redactor := newAct010Redactor()
	redactor.addCookieHeader(rawCookie)
	redactor.addCookieMap(cleanedInitial)
	addAct010TargetsToRedactor(redactor, tg)

	gate, err := sitecheck.OpenGate(filepath.Join(root, ".local", "site-check"), sitecheck.Limits{
		ActID:         act010ID,
		MaxRuns:       act010MaxRuns,
		MaxAPICalls:   act010MaxAPICalls,
		MaxByCategory: act010APILimits,
	})
	if err != nil {
		return rc, err
	}
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}

	ck := &act010Checker{
		gate:           gate,
		apiLimits:      act010APILimits,
		vod:            &mediaBudget{name: "vod", maxRequests: act010MaxVODRequests, maxBytes: act010MaxVODBytes},
		live:           &mediaBudget{name: "live", maxRequests: act010MaxLiveConns, maxBytes: act010MaxLiveBytes},
		minSpacing:     spacing,
		allowLocalhost: cfg.AllowLocalhost,
		statuses:       make(map[string]int),
		apiStatusCodes: make(map[string]map[int]int),
	}

	workDir, err := os.MkdirTemp("", "trace-act010-")
	if err != nil {
		return rc, err
	}
	cleanupWorkDir := func() bool {
		_ = os.RemoveAll(workDir)
		_, statErr := os.Stat(workDir)
		return os.IsNotExist(statErr)
	}
	defer cleanupWorkDir()

	credStore, _, err := credential.OpenDouyin(filepath.Join(workDir, credential.DouyinFileName))
	if err != nil {
		return rc, err
	}

	wrappedTransport := ck.wrap(cfg.Transport)
	dyClient := douyin.NewClient(douyin.ClientOptions{
		CredStore:      credStore,
		Endpoints:      cfg.Endpoints,
		Transport:      wrappedTransport,
		MinInterval:    -1, // Pacing is enforced uniformly across API + CDN + live in act010Checker.
		JitterMax:      -1,
		AllowLocalhost: cfg.AllowLocalhost,
	})
	mediaHTTP := &http.Client{
		Transport: wrappedTransport,
		Timeout:   45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Cookie")
			if len(via) >= 3 {
				return errors.New("media redirect limit exceeded")
			}
			return nil
		},
	}
	liveHTTP := &http.Client{
		Transport: wrappedTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Cookie")
			if len(via) >= 3 {
				return errors.New("live redirect limit exceeded")
			}
			return nil
		},
	}

	rc = act010Receipt{
		Act:            act010Label,
		Commit:         resolveReceiptCommit(root),
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		Verifications:  make(map[string]any),
		APIStatusCodes: ck.apiStatusCodes,
		StatusCounts:   ck.statuses,
		Limits: map[string]any{
			"api":               act010APILimits,
			"apiTotal":          act010MaxAPICalls,
			"shortLinkHops":     0,
			"vodMediaRequests":  act010MaxVODRequests,
			"vodMediaBytes":     act010MaxVODBytes,
			"largeFileCutoff":   act010LargeFileCutoff,
			"liveMediaRequests": act010MaxLiveConns,
			"liveMediaBytes":    act010MaxLiveBytes,
			"recordSeconds":     recordFor.Seconds(),
			"minSpacingMs":      spacing.Milliseconds(),
		},
	}

	ctx, cancel := context.WithTimeout(parentCtx, 4*time.Minute)
	defer cancel()

	runner := newDouyinStepRunner(&rc.Steps, ck, redactor)

	// Step 1: V-01 & V-02 — profile/self guest control (no Cookie) vs authenticated Cookie.
	initialHadMSToken := cleanedInitial["msToken"] != ""
	initialHadUIFID := cleanedInitial["UIFID"] != "" || cleanedInitial["uifid"] != "" || cleanedInitial["UIFID_TEMP"] != ""
	initialHadTTWID := cleanedInitial["ttwid"] != ""
	runner.run("v01_login_control", func(f map[string]any) error {
		ck.guestControlActive.Store(true)
		guestErr := dyClient.CheckProfileSelfGuest(ctx)
		ck.guestControlActive.Store(false)

		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("guest control stopped: %s", stopped)
		}
		guestRejectedAsExpected := guestErr != nil && site.IsKind(guestErr, site.ErrKindAuthRequired)
		ck.mu.Lock()
		guestCode := ck.lastGuestCode
		ck.mu.Unlock()
		f["guestRejectedAuthRequired"] = guestRejectedAsExpected
		f["guestStatusCode"] = guestCode
		if !guestRejectedAsExpected {
			if guestErr != nil {
				return guestErr
			}
			return errors.New("profile/self without Cookie unexpectedly succeeded")
		}

		st, loginErr := dyClient.LoginWithCookie(ctx, rawCookie)
		ck.mu.Lock()
		authCode := ck.lastAuthCode
		ck.mu.Unlock()
		if loginErr != nil {
			f["authStatusCode"] = authCode
			return loginErr
		}
		redactor.addIDs(st.UID, st.SecUID)
		redactor.addHandles(st.UniqueID)
		redactor.addCookieValuesFromStore(credStore)

		hasMSTokenAfter := credStore.MSToken() != ""
		hasTTWIDAfter := credStore.TTWID() != ""
		hasUIFIDAfter := credStore.UIFID() != ""
		f["authStatusCode"] = authCode
		f["loggedIn"] = st.LoggedIn
		f["hasUID"] = st.UID != ""
		f["hasSecUID"] = reAct010SecUID.MatchString(st.SecUID)
		f["hasNickname"] = st.Uname != ""
		f["cookieInitialHadMSToken"] = initialHadMSToken
		f["cookieInitialHadUIFID"] = initialHadUIFID
		f["cookieInitialHadTTWID"] = initialHadTTWID
		f["storeHasMSTokenAfter"] = hasMSTokenAfter
		f["storeHasUIFIDAfter"] = hasUIFIDAfter
		f["storeHasTTWIDAfter"] = hasTTWIDAfter

		rc.Verifications["V-01"] = map[string]any{
			"guestRejectedAuthRequired": guestRejectedAsExpected,
			"guestStatusCode":           guestCode,
			"authStatusCode":            authCode,
			"authProfileComplete":       st.LoggedIn && st.UID != "" && reAct010SecUID.MatchString(st.SecUID) && st.Uname != "",
		}
		return nil
	})

	// Step 2: V-02 & V-03 — profile/other (up to 2 users) + aweme/post (user[0] p1+p2, user[1] p1).
	profilesBySec := make(map[string]*douyin.UserProfile)
	var offlineUserSecUID string
	runner.run("v02_v03_users_and_posts", func(f map[string]any) error {
		var userSummaries []map[string]any
		var v03Summary map[string]any

		for idx, u := range tg.Users {
			prof, err := dyClient.GetUserProfile(ctx, u.SecUID)
			if err != nil {
				return err
			}
			profilesBySec[u.SecUID] = prof
			redactor.addIDs(prof.SecUID, prof.UID, prof.ShortID, prof.RoomID, prof.WebRID)
			redactor.addHandles(prof.UniqueID)
			if (prof.RoomID == "" || prof.RoomID == "0") && offlineUserSecUID == "" {
				offlineUserSecUID = u.SecUID
			}

			seen := make(map[string]bool)
			p1, err := dyClient.ListUserPosts(ctx, douyin.UserPostsRequest{
				SecUID:       u.SecUID,
				MaxCursor:    "0",
				KnownProfile: prof,
				SeenIDs:      seen,
			})
			if err != nil {
				return err
			}
			pinnedCount := 0
			pinnedAllLeading := true
			sawNonPinned := false
			for _, w := range p1.Works {
				redactor.addIDs(w.ID, w.Author.SecUID, w.Author.UID, w.Author.ShortID, w.Author.RoomID, w.Author.WebRID)
				redactor.addHandles(w.Author.UniqueID)
				if w.IsTop {
					pinnedCount++
					if sawNonPinned {
						pinnedAllLeading = false
					}
				} else {
					sawNonPinned = true
				}
			}

			uFact := map[string]any{
				"userIndex":        idx,
				"isPrivate":        prof.IsPrivate,
				"liveStatus":       prof.LiveStatus,
				"hasShortId":       prof.ShortID != "" && prof.ShortID != "0",
				"page1Count":       len(p1.Works),
				"page1PinnedCount": pinnedCount,
				"page1HasMore":     p1.HasMore,
				"uncertainEmpty":   p1.UncertainEmpty,
			}

			if idx == 0 {
				page2Fetched := false
				page2Count := 0
				duplicateAcrossPages := false
				page1IDs := make(map[string]bool, len(p1.Works))
				for _, w := range p1.Works {
					page1IDs[w.ID] = true
				}
				if p1.HasMore && p1.NextCursor != "" && p1.NextCursor != "0" {
					page2Fetched = true
					p2, p2Err := dyClient.ListUserPosts(ctx, douyin.UserPostsRequest{
						SecUID:       u.SecUID,
						MaxCursor:    p1.NextCursor,
						KnownProfile: prof,
						SeenIDs:      seen,
					})
					if p2Err != nil {
						return p2Err
					}
					page2Count = len(p2.Works)
					for _, w := range p2.Works {
						redactor.addIDs(w.ID, w.Author.SecUID, w.Author.UID, w.Author.ShortID)
						redactor.addHandles(w.Author.UniqueID)
						if page1IDs[w.ID] {
							duplicateAcrossPages = true
						}
					}
					uFact["page2Count"] = page2Count
					uFact["page2HasMore"] = p2.HasMore
					uFact["cursorStalled"] = p2.CursorStalled
				}
				v03Summary = map[string]any{
					"page1Items":           len(p1.Works),
					"pinnedCount":          pinnedCount,
					"pinnedLeading":        pinnedAllLeading,
					"page2Fetched":         page2Fetched,
					"page2Items":           page2Count,
					"duplicateAcrossPages": duplicateAcrossPages,
				}
			}
			userSummaries = append(userSummaries, uFact)
		}

		f["users"] = userSummaries
		if v03Summary != nil {
			rc.Verifications["V-03"] = v03Summary
		}
		return nil
	})

	// Step 3: V-02 & V-04/V-05 metadata — aweme/detail for each work in targets.Works (<= 4).
	type fetchedWork struct {
		target act010Work
		detail *douyin.WorkDetail
	}
	var fetchedWorks []fetchedWork
	runner.run("v02_v04_works_detail", func(f map[string]any) error {
		var items []map[string]any
		for idx, w := range tg.Works {
			detail, err := dyClient.GetWorkDetail(ctx, w.AwemeID, douyin.StreamSelectOptions{PreferredCodec: "avc"})
			if err != nil {
				return err
			}
			redactor.addIDs(detail.ID, detail.Author.SecUID, detail.Author.UID, detail.Author.ShortID, detail.Author.RoomID, detail.Author.WebRID)
			redactor.addHandles(detail.Author.UniqueID)
			if detail.Music != nil {
				redactor.addIDs(detail.Music.ID)
			}
			if detail.Kind != w.Kind {
				return fmt.Errorf("work[%d] kind=%q, want %q", idx, detail.Kind, w.Kind)
			}
			if detail.Stats.PlayCount != nil {
				return fmt.Errorf("work[%d] unexpectedly exposed placeholder playCount", idx)
			}

			pageKinds := make(map[string]int)
			for _, pg := range detail.Pages {
				pageKinds[pg.Kind]++
			}
			wFact := map[string]any{
				"workIndex":     idx,
				"declaredKind":  w.Kind,
				"parsedKind":    detail.Kind,
				"expect":        w.Expect,
				"awemeType":     detail.AwemeType,
				"isTop":         detail.IsTop,
				"durationSec":   detail.DurationSec,
				"pageCount":     len(detail.Pages),
				"uncacheable":   detail.Uncacheable,
				"hasMusic":      detail.Music != nil && len(detail.Music.PlayURLs) > 0,
				"playCountZero": false,
			}
			if len(pageKinds) > 0 {
				wFact["pageKinds"] = pageKinds
			}
			if detail.Music != nil {
				wFact["isOriginalSound"] = detail.Music.IsOriginalSound
			}
			if detail.Video != nil {
				wFact["videoQuality"] = detail.Video.QualityLabel
				wFact["videoCodec"] = detail.Video.Codec
				wFact["videoWidth"] = detail.Video.Width
				wFact["videoHeight"] = detail.Video.Height
				wFact["videoDataSize"] = detail.Video.DataSize
			}
			items = append(items, wFact)
			fetchedWorks = append(fetchedWorks, fetchedWork{target: w, detail: detail})
		}
		f["works"] = items
		return nil
	})

	// Step 4: V-04 & V-05 — CDN Range video download (>24 MiB partial probe) + note/mixed assets without Cookie.
	runner.run("v04_v05_media_cdn", func(f map[string]any) error {
		if len(fetchedWorks) == 0 {
			return errSkip
		}
		ck.vodMediaActive.Store(true)
		defer ck.vodMediaActive.Store(false)

		vodTransport := dyClient.VODTransport()

		// 4a. Pick 1 video work for V-05 (prefer "recent" or smaller DataSize).
		var chosenVideo *fetchedWork
		for i := range fetchedWorks {
			fw := &fetchedWorks[i]
			if fw.detail.Kind != douyin.WorkKindVideo || fw.detail.Video == nil || len(fw.detail.Video.URLs) == 0 {
				continue
			}
			if chosenVideo == nil {
				chosenVideo = fw
				continue
			}
			if hasTag(fw.target.Expect, "recent") && !hasTag(chosenVideo.target.Expect, "recent") {
				chosenVideo = fw
			} else if fw.detail.Video.DataSize > 0 && chosenVideo.detail.Video.DataSize > 0 && fw.detail.Video.DataSize < chosenVideo.detail.Video.DataSize {
				chosenVideo = fw
			}
		}

		if chosenVideo != nil {
			vSummary, err := probeOrDownloadMP4(ctx, mediaHTTP, vodTransport, workDir, "v05-video.mp4",
				chosenVideo.detail.Video.URLs[0], chosenVideo.detail.Video.DataSize, float64(chosenVideo.detail.DurationSec), true, ck)
			if err != nil {
				return err
			}
			vSummary["selectedCodec"] = chosenVideo.detail.Video.Codec
			vSummary["selectedQuality"] = chosenVideo.detail.Video.QualityLabel
			f["v05Video"] = vSummary
			rc.Verifications["V-05"] = vSummary
		}

		// 4b. Inspect note & mixed-note assets for V-04 within remaining CDN budget.
		var imageProbes []map[string]any
		var livePhotoSummary map[string]any
		var mixedVideoSummary map[string]any
		var musicSummary map[string]any

		for i := range fetchedWorks {
			fw := &fetchedWorks[i]
			if fw.detail.Kind != douyin.WorkKindNote {
				continue
			}
			probedStaticOnThisNote := false
			for _, pg := range fw.detail.Pages {
				if ck.stopReason() != "" {
					return fmt.Errorf("stopped: %s", ck.stopReason())
				}
				switch pg.Kind {
				case douyin.NotePageImage, douyin.NotePageUnknown:
					if !probedStaticOnThisNote && len(pg.ImageURLs) > 0 && len(imageProbes) < 2 && ck.canAffordVOD(2<<20, 1) {
						imgFact, err := fetchAssetSample(ctx, mediaHTTP, vodTransport, pg.ImageURLs[0], 12<<20)
						if err != nil {
							return err
						}
						imgFact["pageKind"] = pg.Kind
						imageProbes = append(imageProbes, imgFact)
						probedStaticOnThisNote = true
					}
				case douyin.NotePageLivePhoto:
					if !probedStaticOnThisNote && len(pg.ImageURLs) > 0 && len(imageProbes) < 2 && ck.canAffordVOD(2<<20, 1) {
						imgFact, err := fetchAssetSample(ctx, mediaHTTP, vodTransport, pg.ImageURLs[0], 12<<20)
						if err != nil {
							return err
						}
						imgFact["pageKind"] = pg.Kind
						imageProbes = append(imageProbes, imgFact)
						probedStaticOnThisNote = true
					}
					if livePhotoSummary == nil && pg.VideoStream != nil && len(pg.VideoStream.URLs) > 0 && ck.canAffordVOD(9<<20, 2) {
						lpFact, err := probeOrDownloadMP4(ctx, mediaHTTP, vodTransport, workDir, "v04-livephoto.mp4",
							pg.VideoStream.URLs[0], pg.VideoStream.DataSize, float64(pg.VideoStream.DurationSec), false, ck)
						if err != nil {
							return err
						}
						livePhotoSummary = lpFact
					}
				case douyin.NotePageVideo:
					if mixedVideoSummary == nil && pg.VideoStream != nil && len(pg.VideoStream.URLs) > 0 && ck.canAffordVOD(9<<20, 2) {
						mvFact, err := probeOrDownloadMP4(ctx, mediaHTTP, vodTransport, workDir, "v04-note-video.mp4",
							pg.VideoStream.URLs[0], pg.VideoStream.DataSize, float64(pg.VideoStream.DurationSec), false, ck)
						if err != nil {
							return err
						}
						mvFact["selectedCodec"] = pg.VideoStream.Codec
						mvFact["selectedQuality"] = pg.VideoStream.QualityLabel
						mixedVideoSummary = mvFact
					}
				}
			}
			if musicSummary == nil && fw.detail.Music != nil && len(fw.detail.Music.PlayURLs) > 0 && ck.canAffordVOD(4<<20, 1) {
				mFact, err := fetchAssetSample(ctx, mediaHTTP, vodTransport, fw.detail.Music.PlayURLs[0], 16<<20)
				if err != nil {
					return err
				}
				mFact["isOriginalSound"] = fw.detail.Music.IsOriginalSound
				musicSummary = mFact
			}
		}

		v04 := map[string]any{
			"images": imageProbes,
		}
		if livePhotoSummary != nil {
			v04["livePhotoVideo"] = livePhotoSummary
		}
		if mixedVideoSummary != nil {
			v04["mixedNoteVideoPage"] = mixedVideoSummary
		}
		if musicSummary != nil {
			v04["music"] = musicSummary
		}
		f["v04NoteAssets"] = v04
		rc.Verifications["V-04"] = v04
		return nil
	})

	// Step 5: V-06 (reflow/info offline & online) + V-08 (two liveRooms forms comparison).
	var activeRoom *douyin.LiveRoomDetail
	runner.run("v06_v08_live_rooms_and_reflow", func(f map[string]any) error {
		type formOutcome struct {
			form     string
			queried  string
			accepted bool
			errKind  string
			detail   *douyin.LiveRoomDetail
		}
		var outcomes []formOutcome
		formsTested := make([]string, 0, len(tg.LiveRooms))
		acceptedByForm := make(map[string]bool, len(tg.LiveRooms))
		respMatchesQueried := make(map[string]bool, len(tg.LiveRooms))

		for idx, lr := range tg.LiveRooms {
			formLabel := lr.Form
			if formLabel == "" {
				formLabel = fmt.Sprintf("form_%d", idx)
			}
			formsTested = append(formsTested, formLabel)

			detail, err := dyClient.EnterLiveRoom(ctx, lr.WebRID, "")
			if err != nil {
				if ck.stopReason() != "" || site.IsRoundStop(err) || errors.Is(err, sitecheck.ErrQuotaExceeded) {
					return err
				}
				acceptedByForm[formLabel] = false
				outcomes = append(outcomes, formOutcome{
					form:     formLabel,
					queried:  lr.WebRID,
					accepted: false,
					errKind:  site.KindOf(err),
				})
				continue
			}
			redactor.addIDs(detail.OwnerSecUID, detail.OwnerUID, detail.OwnerShortID, detail.QueriedWebRID, detail.ResponseWebRID, detail.RoomID)
			acceptedByForm[formLabel] = true
			respMatchesQueried[formLabel] = detail.ResponseWebRID == lr.WebRID
			outcomes = append(outcomes, formOutcome{
				form:     formLabel,
				queried:  lr.WebRID,
				accepted: true,
				detail:   detail,
			})
			if activeRoom == nil || (activeRoom.RoomStatus != 2 && detail.RoomStatus == 2) {
				activeRoom = detail
			}
		}

		allAccepted := len(outcomes) > 0
		sameOwnerSecUID := true
		sameRoomID := true
		var firstAccepted *douyin.LiveRoomDetail
		var shortIDFormMatchesOwnerShortID *bool
		var shortIDMatchesProfileOther *bool

		for i := range outcomes {
			o := &outcomes[i]
			if !o.accepted {
				allAccepted = false
				sameOwnerSecUID = false
				sameRoomID = false
				continue
			}
			if firstAccepted == nil {
				firstAccepted = o.detail
			} else {
				if o.detail.OwnerSecUID == "" || o.detail.OwnerSecUID != firstAccepted.OwnerSecUID {
					sameOwnerSecUID = false
				}
				if o.detail.RoomID != firstAccepted.RoomID {
					sameRoomID = false
				}
			}
			if o.form == "short_id" {
				matched := o.detail.OwnerShortID != "" && o.detail.OwnerShortID == o.queried
				shortIDFormMatchesOwnerShortID = &matched
				if prof, ok := profilesBySec[o.detail.OwnerSecUID]; ok {
					profMatch := prof.ShortID != "" && prof.ShortID == o.queried
					shortIDMatchesProfileOther = &profMatch
				}
			}
		}

		roomStatus := 0
		audioOnly := false
		if activeRoom != nil {
			roomStatus = activeRoom.RoomStatus
			audioOnly = activeRoom.AudioOnly
		}

		v08 := map[string]any{
			"formsTested":                  formsTested,
			"acceptedByForm":               acceptedByForm,
			"bothAccepted":                 allAccepted && len(outcomes) >= 2,
			"sameOwnerSecUid":              allAccepted && len(outcomes) >= 2 && sameOwnerSecUID,
			"sameRoomId":                   allAccepted && len(outcomes) >= 2 && sameRoomID,
			"responseWebRidMatchesQueried": respMatchesQueried,
			"roomStatus":                   roomStatus,
			"audioOnly":                    audioOnly,
		}
		if shortIDFormMatchesOwnerShortID != nil {
			v08["shortIdFormMatchesOwnerShortId"] = *shortIDFormMatchesOwnerShortID
		}
		if shortIDMatchesProfileOther != nil {
			v08["shortIdMatchesProfileOther"] = *shortIDMatchesProfileOther
		}
		f["v08"] = v08
		rc.Verifications["V-08"] = v08

		// V-06: Offline reflow/info (room_id=2) check + optional online reflow/info check.
		v06TargetSec := offlineUserSecUID
		if activeRoom != nil && activeRoom.RoomStatus != 2 && activeRoom.OwnerSecUID != "" {
			v06TargetSec = activeRoom.OwnerSecUID
		}
		if v06TargetSec == "" && len(tg.Users) > 0 {
			v06TargetSec = tg.Users[0].SecUID
		}

		v06 := map[string]any{}
		if v06TargetSec != "" {
			offDetail, offErr := dyClient.ReflowRoomInfo(ctx, "2", v06TargetSec)
			if offErr != nil {
				if ck.stopReason() != "" || site.IsRoundStop(offErr) || errors.Is(offErr, sitecheck.ErrQuotaExceeded) {
					return offErr
				}
				v06["offlineRoomId2Accepted"] = false
				v06["offlineRoomId2YieldsWebRid"] = false
				v06["offlineRoomId2ErrorKind"] = site.KindOf(offErr)
			} else {
				redactor.addIDs(offDetail.OwnerSecUID, offDetail.OwnerUID, offDetail.OwnerShortID, offDetail.ResponseWebRID, offDetail.RoomID)
				v06["offlineRoomId2Accepted"] = true
				v06["offlineRoomId2YieldsWebRid"] = offDetail.ResponseWebRID != ""
			}
		}

		if activeRoom != nil && activeRoom.RoomStatus == 2 && activeRoom.RoomID != "" {
			onDetail, onErr := dyClient.ReflowRoomInfo(ctx, activeRoom.RoomID, activeRoom.OwnerSecUID)
			if onErr != nil {
				if ck.stopReason() != "" || site.IsRoundStop(onErr) || errors.Is(onErr, sitecheck.ErrQuotaExceeded) {
					return onErr
				}
				v06["onlineReflowAccepted"] = false
				v06["onlineReflowErrorKind"] = site.KindOf(onErr)
			} else {
				redactor.addIDs(onDetail.OwnerSecUID, onDetail.OwnerUID, onDetail.OwnerShortID, onDetail.ResponseWebRID, onDetail.RoomID)
				v06["onlineReflowAccepted"] = true
				v06["onlineReflowYieldsWebRid"] = onDetail.ResponseWebRID != ""
				v06["onlineReflowOwnerMatches"] = onDetail.OwnerSecUID == activeRoom.OwnerSecUID
			}
		}

		f["v06"] = v06
		rc.Verifications["V-06"] = v06
		return nil
	})

	// Step 6: V-07 — Live stream inspection (HLS .m3u8 extension + HTTP-FLV wire header & <=25s recording).
	runner.run("v07_live_stream_and_record", func(f map[string]any) error {
		return probeAndRecordDouyinLive(ctx, activeRoom, dyClient, mediaHTTP, liveHTTP, gate, ck, redactor, &rc, f, douyinLiveProbeOptions{
			allowLocalhost: cfg.AllowLocalhost,
			fetchHLS:       true,
			recordFor:      recordFor,
			workDir:        workDir,
		})
	})

	// Step V-02 rollup across all executed signed & Argus-protected endpoints.
	if ck.stopReason() == "" {
		rc.Verifications["V-02"] = map[string]any{
			"passedAllSignedEndpoints": true,
			"cookieInitialHadMSToken":  initialHadMSToken,
			"cookieInitialHadUIFID":    initialHadUIFID,
			"cookieInitialHadTTWID":    initialHadTTWID,
			"apiCallsByCategory":       gate.Usage().Categories,
		}
	} else {
		rc.Verifications["V-02"] = map[string]any{
			"passedAllSignedEndpoints": false,
			"stoppedBy":                ck.stopReason(),
		}
	}

	err = finalizeAct010Receipt(act010ID, &rc, outPath, gate, ck, redactor, cleanupWorkDir())
	return rc, err
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), want) {
			return true
		}
	}
	return false
}
