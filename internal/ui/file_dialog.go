package ui

import (
	"context"
	"errors"
	"strings"

	"github.com/chialecode/trace/internal/service"
)

// FileDialogKind selects the native OS file or directory picker mode.
type FileDialogKind string

const (
	FileDialogPickDirectory FileDialogKind = "pick_directory"
	FileDialogOpenFile      FileDialogKind = "open_file"
	FileDialogSaveFile      FileDialogKind = "save_file"
)

// FileDialogFilter limits visible files in open/save native file dialogs.
type FileDialogFilter struct {
	Name       string
	Extensions []string
}

// FileDialogOptions configures a native OS file or directory dialog.
type FileDialogOptions struct {
	Kind        FileDialogKind
	Title       string
	DefaultPath string
	// DefaultName suggests a file name for FileDialogSaveFile when DefaultPath
	// is empty or points to an existing directory.
	DefaultName string
	Filters     []FileDialogFilter
}

// FileDialogHandler shows a native OS file or directory picker and returns the
// selected path, or ("", nil) when canceled or suppressed (e.g. quiet mode).
// Implementations may block and are always invoked off the UI thread when an
// event-loop dispatcher is bound.
type FileDialogHandler func(opts FileDialogOptions) (string, error)

const subSnapshotDefaultName = "trace-subscriptions.json"

var jsonFileDialogFilters = []FileDialogFilter{
	{Name: "JSON 文件", Extensions: []string{"json"}},
	{Name: "所有文件", Extensions: []string{"*"}},
}

var errFileDialogUnavailable = errors.New("当前环境暂不支持打开系统选择框，请手动输入路径")

// BindFileDialog connects native OS file/folder pickers to the shell window.
// Memory-only UI tests inject a mock handler so zero OS dialogs ever open.
func (a *App) BindFileDialog(fn FileDialogHandler) {
	a.mu.Lock()
	a.fileDialog = fn
	a.mu.Unlock()
}

func (a *App) fileDialogBusy() bool {
	return a.isPending("file-dialog")
}

// pickFilePath runs the bound FileDialogHandler asynchronously via
// requestGuarded (off the UI thread when a dispatcher is bound) and writes a
// non-empty selection back only while current() still holds. Empty/canceled
// results leave the target input untouched.
func (a *App) pickFilePath(opts FileDialogOptions, current func() bool, onPicked func(string), onErr func(error)) {
	a.mu.Lock()
	handler := a.fileDialog
	a.mu.Unlock()
	if handler == nil {
		if onErr != nil {
			onErr(errFileDialogUnavailable)
		} else {
			a.showError(errFileDialogUnavailable)
		}
		return
	}
	requestGuarded(a, "file-dialog", current, func(context.Context) (string, error) {
		return handler(opts)
	}, func(picked string) {
		picked = strings.TrimSpace(picked)
		if picked == "" {
			return
		}
		if onPicked != nil {
			onPicked(picked)
		}
	}, func(err error) {
		if onErr != nil {
			onErr(err)
			return
		}
		a.showError(err)
	})
}

func (a *App) pickCacheDir() {
	opts := FileDialogOptions{
		Kind:        FileDialogPickDirectory,
		Title:       "选择缓存保存位置",
		DefaultPath: a.CacheDirInput,
	}
	current := func() bool {
		return a.CurrentRoute == RouteSettings &&
			a.SettingsCategory == "storage" &&
			!a.isPending("storage-migrate")
	}
	a.pickFilePath(opts, current, func(picked string) {
		a.CacheDirInput = picked
	}, nil)
}

func (a *App) pickAddLibraryRoot() {
	opts := FileDialogOptions{
		Kind:        FileDialogPickDirectory,
		Title:       "选择已有的缓存文件夹",
		DefaultPath: a.AddLibraryRootInput,
	}
	current := func() bool {
		return a.CurrentRoute == RouteSettings &&
			a.SettingsCategory == "storage" &&
			!a.isPending("storage-add-root")
	}
	a.pickFilePath(opts, current, func(picked string) {
		a.AddLibraryRootInput = picked
	}, nil)
}

func (a *App) pickTransferImportDir() {
	gen := a.transferImportGen
	opts := FileDialogOptions{
		Kind:        FileDialogPickDirectory,
		Title:       "选择要导入的文件夹",
		DefaultPath: a.Transfer.ImportPath,
	}
	current := func() bool {
		if a.CurrentRoute != RouteLibrary || !a.Transfer.ImportPage || a.transferImportGen != gen {
			return false
		}
		if job, ok := a.transferJob(a.Transfer.JobID); ok && job.Status == service.TransferScanning {
			return false
		}
		return true
	}
	a.pickFilePath(opts, current, func(picked string) {
		a.Transfer.ImportPath = picked
		a.Transfer.ImportErr = ""
	}, func(err error) {
		a.Transfer.ImportErr = userErrorText(err)
	})
}

func (a *App) pickTransferExportDir() {
	gen := a.transferExportGen
	opts := FileDialogOptions{
		Kind:        FileDialogPickDirectory,
		Title:       "选择导出目标文件夹",
		DefaultPath: a.Transfer.ExportPath,
	}
	current := func() bool {
		return a.Transfer.ExportOpen && a.transferExportGen == gen
	}
	a.pickFilePath(opts, current, func(picked string) {
		a.Transfer.ExportPath = picked
		a.Transfer.ExportErr = ""
	}, func(err error) {
		a.Transfer.ExportErr = userErrorText(err)
	})
}

func (a *App) pickSubExportFile() {
	gen := a.subExportGen
	opts := FileDialogOptions{
		Kind:        FileDialogSaveFile,
		Title:       "导出订阅",
		DefaultPath: a.SubFile.ExportPath,
		DefaultName: subSnapshotDefaultName,
		Filters:     jsonFileDialogFilters,
	}
	current := func() bool {
		return a.SubFile.ExportOpen && a.subExportGen == gen
	}
	a.pickFilePath(opts, current, func(picked string) {
		a.SubFile.ExportPath = picked
		a.SubFile.Err = ""
	}, func(err error) {
		a.SubFile.Err = userErrorText(err)
	})
}

func (a *App) pickSubImportFile() {
	gen := a.subImportGen
	opts := FileDialogOptions{
		Kind:        FileDialogOpenFile,
		Title:       "选择订阅文件",
		DefaultPath: a.SubFile.ImportPath,
		Filters:     jsonFileDialogFilters,
	}
	current := func() bool {
		return a.SubFile.ImportOpen &&
			a.subImportGen == gen &&
			!a.isPending("sub-import-preview") &&
			!a.isPending("sub-import-apply")
	}
	a.pickFilePath(opts, current, func(picked string) {
		a.SubFile.ImportPath = picked
		a.subImportGen++
		a.SubFile.Preview = nil
		a.SubFile.Err = ""
	}, func(err error) {
		a.SubFile.Err = userErrorText(err)
	})
}
