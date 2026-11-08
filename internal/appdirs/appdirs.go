package appdirs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Build-time variables injected by tools/package and tools/release through
// -ldflags -X (MYGO_LDFLAGS); see BuildInfo.
var (
	channel     = ""
	ChannelName = "stable"
	version     = ""
	commit      = ""
	buildTime   = ""
)

// UnversionedBuild is the version of builds without injected build
// information: go run, go test and mygo dev.
const UnversionedBuild = "0.0.0-dev"

// Build describes the running build: its version, channel, commit and UTC
// build time, as tools/package and tools/release computed them.
type Build struct {
	Version   string `json:"version"`
	Channel   string `json:"channel"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
}

// BuildInfo returns the injected build information; a build without it
// reports UnversionedBuild and an empty commit and time.
func BuildInfo() Build {
	v := strings.TrimSpace(version)
	if v == "" {
		v = UnversionedBuild
	}
	return Build{
		Version:   v,
		Channel:   EffectiveChannel(""),
		Commit:    strings.TrimSpace(commit),
		BuildTime: strings.TrimSpace(buildTime),
	}
}

// CompiledChannel returns the channel the build was made for, ignoring the
// TRACE_CHANNEL override that tests and tools use: "stable", "dev" or
// "ptest". Builds without an injected channel follow ChannelName, as
// EffectiveChannel does.
func CompiledChannel() string {
	ch := strings.ToLower(strings.TrimSpace(channel))
	if ch == "" {
		ch = strings.ToLower(strings.TrimSpace(ChannelName))
	}
	switch ch {
	case "dev", "development", "source":
		return "dev"
	case "ptest":
		return "ptest"
	default:
		return "stable"
	}
}

// ChannelLabel is the user-facing name of a channel.
func ChannelLabel(channel string) string {
	switch EffectiveChannel(channel) {
	case "dev":
		return "开发版"
	case "ptest":
		return "测试身份"
	default:
		return "正式版"
	}
}

// ErrAlreadyLocked is returned when another process holds the data directory lock.
var ErrAlreadyLocked = errors.New("appdirs: data directory is already locked by another instance")

// Locations describes resolved runtime directories and identifiers.
type Locations struct {
	Mode            string `json:"mode"`
	Channel         string `json:"channel"`
	AppName         string `json:"appName"`
	DisplayName     string `json:"displayName"`
	Identifier      string `json:"identifier"`
	InstallDir      string `json:"installDir"`
	DataDir         string `json:"dataDir"`
	DefaultCacheDir string `json:"defaultCacheDir"`
	CredentialsDir  string `json:"credentialsDir"`
	LogsDir         string `json:"logsDir"`
	TempDir         string `json:"tempDir"`
	LockFile        string `json:"lockFile"`
}

// Options allows overriding environment/executable detection in tests and tools.
type Options struct {
	Channel      string
	ExePath      string
	ForceMode    string // "portable" or "installed"
	DataDir      string
	CacheDir     string
	LocalAppData string
	DocumentsDir string
}

// EffectiveChannel returns the normalized channel ("stable", "dev", or "ptest").
func EffectiveChannel(override string) string {
	ch := strings.ToLower(strings.TrimSpace(override))
	if ch == "" {
		ch = strings.ToLower(strings.TrimSpace(os.Getenv("TRACE_CHANNEL")))
	}
	if ch == "" {
		ch = strings.ToLower(strings.TrimSpace(channel))
	}
	if ch == "" {
		ch = strings.ToLower(strings.TrimSpace(ChannelName))
	}
	switch ch {
	case "dev", "development", "source":
		return "dev"
	case "ptest":
		return "ptest"
	default:
		return "stable"
	}
}

// AppNameForChannel returns the strictly lowercase application name for a given channel.
func AppNameForChannel(channel string) string {
	switch EffectiveChannel(channel) {
	case "dev":
		return "trace-dev"
	case "ptest":
		return "trace-ptest"
	default:
		return "trace"
	}
}

// DisplayNameForChannel returns the human-facing display name for a given channel.
func DisplayNameForChannel(channel string) string {
	switch EffectiveChannel(channel) {
	case "dev":
		return "TRACE Dev"
	case "ptest":
		return "TRACE PTest"
	default:
		return "TRACE"
	}
}

// IdentifierForChannel returns the lowercase application identifier for a given channel.
func IdentifierForChannel(channel string) string {
	switch EffectiveChannel(channel) {
	case "dev":
		return "io.github.chialecode.trace-dev"
	case "ptest":
		return "io.github.chialecode.trace-ptest"
	default:
		return "io.github.chialecode.trace"
	}
}

// Resolve computes Locations using the current process environment.
func Resolve() (Locations, error) {
	return ResolveWithOptions(Options{})
}

// ResolveWithOptions computes Locations with optional test/tool overrides.
func ResolveWithOptions(opts Options) (Locations, error) {
	ch := EffectiveChannel(opts.Channel)
	appName := AppNameForChannel(ch)
	displayName := DisplayNameForChannel(ch)
	identifier := IdentifierForChannel(ch)

	exePath := opts.ExePath
	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			exePath = "."
		}
	}
	installDir := filepath.Clean(filepath.Dir(exePath))

	mode := strings.ToLower(strings.TrimSpace(opts.ForceMode))
	if mode == "" {
		if os.Getenv("TRACE_PORTABLE") == "1" {
			mode = "portable"
		} else if os.Getenv("TRACE_PORTABLE") == "0" {
			mode = "installed"
		} else if fileExists(filepath.Join(installDir, "portable.flag")) {
			mode = "portable"
		} else {
			mode = "installed"
		}
	}

	dataDir := strings.TrimSpace(opts.DataDir)
	if dataDir == "" {
		dataDir = strings.TrimSpace(os.Getenv("TRACE_DATA_DIR"))
	}
	if dataDir == "" {
		if mode == "portable" {
			dataDir = filepath.Join(installDir, "trace-data")
		} else {
			localAppData := strings.TrimSpace(opts.LocalAppData)
			if localAppData == "" {
				localAppData = strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
			}
			if localAppData == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return Locations{}, fmt.Errorf("appdirs: resolve home dir: %w", err)
				}
				localAppData = filepath.Join(home, "AppData", "Local")
			}
			dataDir = filepath.Join(localAppData, appName)
		}
	}
	dataDir = filepath.Clean(dataDir)

	defaultCacheDir := strings.TrimSpace(opts.CacheDir)
	if defaultCacheDir == "" {
		defaultCacheDir = strings.TrimSpace(os.Getenv("TRACE_CACHE_DIR"))
	}
	if defaultCacheDir == "" {
		if mode == "portable" {
			defaultCacheDir = filepath.Join(dataDir, "library")
		} else {
			docsDir := strings.TrimSpace(opts.DocumentsDir)
			if docsDir == "" {
				docsDir = strings.TrimSpace(os.Getenv("TRACE_DOCUMENTS_DIR"))
			}
			if docsDir == "" {
				var err error
				docsDir, err = resolveDocumentsDir()
				if err != nil || docsDir == "" {
					home, _ := os.UserHomeDir()
					docsDir = filepath.Join(home, "Documents")
				}
			}
			defaultCacheDir = filepath.Join(docsDir, appName)
		}
	}
	defaultCacheDir = filepath.Clean(defaultCacheDir)

	return Locations{
		Mode:            mode,
		Channel:         ch,
		AppName:         appName,
		DisplayName:     displayName,
		Identifier:      identifier,
		InstallDir:      installDir,
		DataDir:         dataDir,
		DefaultCacheDir: defaultCacheDir,
		CredentialsDir:  filepath.Join(dataDir, "credentials"),
		LogsDir:         filepath.Join(dataDir, "logs"),
		TempDir:         filepath.Join(dataDir, "temp"),
		LockFile:        filepath.Join(dataDir, "app.lock"),
	}, nil
}

// EnsureDirectories creates DataDir, its required subdirectories, and DefaultCacheDir.
func EnsureDirectories(loc Locations) error {
	dirs := []string{
		loc.DataDir,
		loc.CredentialsDir,
		loc.LogsDir,
		loc.TempDir,
		loc.DefaultCacheDir,
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("appdirs: mkdir %s: %w", d, err)
		}
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
