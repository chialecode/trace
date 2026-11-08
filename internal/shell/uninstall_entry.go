package shell

import (
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/appdirs"
)

// uninstallEntry is the per-user uninstall entry of an identity, which
// "设置 › 应用" lists. An update replaces the program files only, so the
// first launch of the new version writes its version into the entry.
type uninstallEntry interface {
	Read(identifier string) (location, version string, ok bool, err error)
	SetVersion(identifier, version string) error
}

// syncUninstallVersion writes version into the uninstall entry of the
// identity when that entry is this installation's and names another
// version. It reports whether it wrote.
func syncUninstallVersion(store uninstallEntry, identifier, installDir, version string) (bool, error) {
	if store == nil || identifier == "" || version == "" || version == appdirs.UnversionedBuild {
		return false, nil
	}
	location, current, ok, err := store.Read(identifier)
	if err != nil || !ok {
		return false, err
	}
	if !strings.EqualFold(filepath.Clean(strings.Trim(location, `"`)), filepath.Clean(installDir)) || current == version {
		return false, nil
	}
	return true, store.SetVersion(identifier, version)
}
