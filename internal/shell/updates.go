package shell

import (
	"context"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/chialecode/trace/internal/update"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"
)

// Command-line flags of TRACE (architecture §8).
const (
	// FlagQuitForUpdate asks the running TRACE to save its work and quit so
	// that tools/package can install a new development build.
	FlagQuitForUpdate = "--quit-for-update"
	// FlagBackground starts TRACE in the tray without opening its window.
	FlagBackground = "--background"
)

// PtestUpdateURLEnv points the ptest identity at a local release; other
// channels ignore it.
const PtestUpdateURLEnv = "TRACE_PTEST_UPDATE_URL"

// restartMarkerTTL is how long a quit for an update decides how the next
// launch opens: a later manual launch opens the window as usual.
const restartMarkerTTL = 5 * time.Minute

// ptestFirstCheckDelay is when the ptest identity, pointed at a local
// release by TRACE_PTEST_UPDATE_URL, checks for the update of the
// rehearsal.
const ptestFirstCheckDelay = 3 * time.Second

// LaunchArgs are the flags TRACE was started (or a second instance) with.
type LaunchArgs struct {
	QuitForUpdate bool
	Background    bool
}

// ParseLaunchArgs reads the flags TRACE knows from a command line.
func ParseLaunchArgs(args []string) LaunchArgs {
	var la LaunchArgs
	for _, a := range args {
		switch strings.ToLower(strings.TrimSpace(a)) {
		case FlagQuitForUpdate:
			la.QuitForUpdate = true
		case FlagBackground:
			la.Background = true
		}
	}
	return la
}

// StartupDecision is what a launch does with its window and which update
// notice it shows, from the launch flags and ui-state.json.
type StartupDecision struct {
	OpenWindow   bool
	UpdatedTo    string
	UpdatedNotes string
}

// DecideStartup applies the launch rules and returns the state to save:
// a recent quit for an update reopens the window as it was before it;
// otherwise --background starts in the tray and any other launch opens the
// window. A launch of a newer version than the last one says once that
// TRACE was updated, with the notes of the downloaded release.
func DecideStartup(st uistate.State, args LaunchArgs, version string, now time.Time) (StartupDecision, uistate.State) {
	d := StartupDecision{OpenWindow: !args.Background}
	if r := st.Update.Restart; r != nil {
		if at, err := time.Parse(time.RFC3339, r.At); err == nil && now.Sub(at) >= 0 && now.Sub(at) <= restartMarkerTTL {
			d.OpenWindow = r.WindowOpen
		}
		st.Update.Restart = nil
	}
	prev := st.Update.LastRunVersion
	if version != appdirs.UnversionedBuild && prev != "" && prev != appdirs.UnversionedBuild && update.Compare(version, prev) > 0 {
		d.UpdatedTo = version
		if st.Update.ReadyVersion == version {
			d.UpdatedNotes = st.Update.ReadyNotes
		}
	}
	if version != appdirs.UnversionedBuild {
		st.Update.LastRunVersion = version
	}
	if st.Update.ReadyVersion != "" && update.Compare(version, st.Update.ReadyVersion) >= 0 {
		st.Update.ReadyVersion, st.Update.ReadyNotes = "", ""
	}
	return d, st
}

// mygoEngine adapts mygo.Updater to update.Engine.
type mygoEngine struct {
	mu   sync.Mutex
	last *mygo.Update
}

func (e *mygoEngine) Enabled() bool { return mygo.Updater.Enabled() }

func (e *mygoEngine) Check(ctx context.Context) (*update.Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	up, err := mygo.Updater.Check(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.last = up
	e.mu.Unlock()
	if up == nil {
		return nil, nil
	}
	return &update.Release{Version: up.Version, Notes: up.Notes, Date: up.Date}, nil
}

func (e *mygoEngine) Install(ctx context.Context, progress func(int64, int64)) error {
	e.mu.Lock()
	up := e.last
	e.mu.Unlock()
	if up == nil {
		return update.ErrRejected
	}
	return up.Install(ctx, progress)
}

// newUpdateController wires the updates of the running build: the release
// channel (and the ptest identity of the rehearsal) reads the feed that
// tools/release injected through TRACE's verified source; builds without
// updates get a disabled controller.
func newUpdateController(backend *service.Backend, onChange func(), restart func()) *update.Controller {
	build := appdirs.BuildInfo()
	var engine update.Engine
	var firstDelay time.Duration
	feed, key := update.Feed()
	if feed != "" && key != "" {
		if pub, err := update.ParsePublicKey(key); err == nil {
			src := &update.Source{
				ManifestURL: feed,
				PublicKey:   pub,
				Current:     build.Version,
				Client:      &http.Client{Transport: netproxy.Transport(), Timeout: 15 * time.Minute},
			}
			if appdirs.CompiledChannel() == "ptest" {
				if base := strings.TrimRight(strings.TrimSpace(os.Getenv(PtestUpdateURLEnv)), "/"); base != "" {
					src.ManifestURL = base + "/" + path.Base(feed)
					src.AllowLoopbackHTTP = true
					src.Rewrite = func(raw string) string { return base + "/" + path.Base(raw) }
					firstDelay = ptestFirstCheckDelay
				}
			}
			mygo.Updater.SetSource(src)
			engine = &mygoEngine{}
		} else {
			backend.Logf("updates disabled: %v", err)
		}
	}
	uiSt := backend.GetUIState()
	lastCheck, _ := time.Parse(time.RFC3339, uiSt.Update.LastCheck)
	return update.NewController(update.Options{
		Engine:         engine,
		Channel:        appdirs.CompiledChannel(),
		CurrentVersion: build.Version,
		LastCheck:      lastCheck,
		Prefs: func() update.Prefs {
			p := backend.GetSettings().Updates
			return update.Prefs{AutoCheck: p.AutoCheck, AutoDownload: p.AutoDownload}
		},
		OnChange: onChange,
		Logf:     backend.Logf,
		SaveLastCheck: func(t time.Time) {
			_, _ = backend.MutateUIState(func(st *uistate.State) { st.Update.LastCheck = t.UTC().Format(time.RFC3339) })
		},
		SaveReady: func(version, notes string) {
			_, _ = backend.MutateUIState(func(st *uistate.State) {
				st.Update.ReadyVersion, st.Update.ReadyNotes = version, notes
			})
		},
		ActiveWork: backend.ActiveWork,
		Restart:    restart,
		FirstDelay: firstDelay,
	})
}
