package shell

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/ui"
	"github.com/egoist/mygo"
)

func TestShellFileDialogQuietAndClosedGuards(t *testing.T) {
	openCalled := false
	saveCalled := false
	sh := &Shell{
		quiet: "hidden",
		openFileDialog: func(mygo.OpenDialogOptions) ([]string, error) {
			openCalled = true
			return []string{"/should/not/happen"}, nil
		},
		saveFileDialog: func(mygo.SaveDialogOptions) (string, error) {
			saveCalled = true
			return "/should/not/happen.json", nil
		},
	}

	winClosed := &atomic.Bool{}
	handler := sh.newFileDialogHandler(nil, winClosed)

	// 1. Quiet mode "hidden" suppresses all dialog kinds.
	for _, kind := range []ui.FileDialogKind{ui.FileDialogPickDirectory, ui.FileDialogOpenFile, ui.FileDialogSaveFile} {
		got, err := handler(ui.FileDialogOptions{Kind: kind, Title: "test"})
		if err != nil || got != "" {
			t.Fatalf("quiet=hidden kind=%s: got (%q, %v), want (\"\", nil)", kind, got, err)
		}
	}
	if openCalled || saveCalled {
		t.Fatal("quiet=hidden must never invoke OS dialog functions")
	}

	// 2. Quiet mode "inactive" also suppresses dialogs.
	sh.quiet = "inactive"
	if got, err := handler(ui.FileDialogOptions{Kind: ui.FileDialogPickDirectory}); err != nil || got != "" {
		t.Fatalf("quiet=inactive: got (%q, %v), want (\"\", nil)", got, err)
	}
	if openCalled || saveCalled {
		t.Fatal("quiet=inactive must never invoke OS dialog functions")
	}

	// 3. Closed window suppresses dialogs even in interactive mode.
	sh.quiet = ""
	winClosed.Store(true)
	if got, err := handler(ui.FileDialogOptions{Kind: ui.FileDialogOpenFile}); err != nil || got != "" {
		t.Fatalf("winClosed=true: got (%q, %v), want (\"\", nil)", got, err)
	}
	if openCalled || saveCalled {
		t.Fatal("closed window must never invoke OS dialog functions")
	}

	// 4. Quitting shell suppresses dialogs even if winClosed is false.
	winClosed.Store(false)
	sh.quitting = true
	if got, err := handler(ui.FileDialogOptions{Kind: ui.FileDialogSaveFile}); err != nil || got != "" {
		t.Fatalf("quitting=true: got (%q, %v), want (\"\", nil)", got, err)
	}
	if openCalled || saveCalled {
		t.Fatal("quitting shell must never invoke OS dialog functions")
	}
}

func TestShellFileDialogInteractiveOptionsAndDefaultPaths(t *testing.T) {
	tmpDir := t.TempDir()
	existingFile := filepath.Join(tmpDir, "subs.json")
	if err := os.WriteFile(existingFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotOpen mygo.OpenDialogOptions
	var gotSave mygo.SaveDialogOptions
	var openReturn []string
	var openErr error
	var saveReturn string
	var saveErr error

	sh := &Shell{
		openFileDialog: func(o mygo.OpenDialogOptions) ([]string, error) {
			gotOpen = o
			return openReturn, openErr
		},
		saveFileDialog: func(o mygo.SaveDialogOptions) (string, error) {
			gotSave = o
			return saveReturn, saveErr
		},
	}
	winClosed := &atomic.Bool{}
	handler := sh.newFileDialogHandler(nil, winClosed)

	// 1. PickDirectory with existing folder (wrapped in quotes).
	openReturn, openErr = []string{filepath.Join(tmpDir, "picked")}, nil
	got, err := handler(ui.FileDialogOptions{
		Kind:        ui.FileDialogPickDirectory,
		Title:       "选择目录",
		DefaultPath: ` "` + tmpDir + `" `,
	})
	if err != nil || got != filepath.Join(tmpDir, "picked") {
		t.Fatalf("PickDirectory got (%q, %v)", got, err)
	}
	if !gotOpen.Directory || !gotOpen.CreateDirectories || gotOpen.Title != "选择目录" || gotOpen.DefaultPath != tmpDir {
		t.Fatalf("unexpected OpenDialogOptions for PickDirectory: %+v", gotOpen)
	}

	// PickDirectory with non-existent child falls back to existing parent.
	openReturn, openErr = nil, nil
	got, err = handler(ui.FileDialogOptions{
		Kind:        ui.FileDialogPickDirectory,
		DefaultPath: filepath.Join(tmpDir, "not-yet-created"),
	})
	if err != nil || got != "" {
		t.Fatalf("PickDirectory cancel got (%q, %v), want (\"\", nil)", got, err)
	}
	if gotOpen.DefaultPath != tmpDir {
		t.Fatalf("PickDirectory fallback DefaultPath = %q, want %q", gotOpen.DefaultPath, tmpDir)
	}

	// 2. OpenFile passes filters and existing file DefaultPath.
	openReturn, openErr = []string{existingFile}, nil
	got, err = handler(ui.FileDialogOptions{
		Kind:        ui.FileDialogOpenFile,
		Title:       "选择订阅文件",
		DefaultPath: existingFile,
		Filters: []ui.FileDialogFilter{
			{Name: "JSON 文件", Extensions: []string{"json"}},
		},
	})
	if err != nil || got != existingFile {
		t.Fatalf("OpenFile got (%q, %v)", got, err)
	}
	if gotOpen.Directory || gotOpen.DefaultPath != existingFile || len(gotOpen.Filters) != 1 || gotOpen.Filters[0].Extensions[0] != "json" {
		t.Fatalf("unexpected OpenDialogOptions for OpenFile: %+v", gotOpen)
	}

	// 3. SaveFile with existing dir appends DefaultName.
	wantSaveDefault := filepath.Join(tmpDir, "trace-subscriptions.json")
	saveReturn, saveErr = wantSaveDefault, nil
	got, err = handler(ui.FileDialogOptions{
		Kind:        ui.FileDialogSaveFile,
		Title:       "导出订阅",
		DefaultPath: tmpDir,
		DefaultName: "trace-subscriptions.json",
		Filters: []ui.FileDialogFilter{
			{Name: "JSON 文件", Extensions: []string{"json"}},
		},
	})
	if err != nil || got != wantSaveDefault {
		t.Fatalf("SaveFile got (%q, %v)", got, err)
	}
	if !gotSave.CreateDirectories || gotSave.DefaultPath != wantSaveDefault || len(gotSave.Filters) != 1 {
		t.Fatalf("unexpected SaveDialogOptions for SaveFile: %+v", gotSave)
	}

	// SaveFile with empty DefaultPath uses DefaultName alone.
	saveReturn, saveErr = "", errors.New("save failed")
	got, err = handler(ui.FileDialogOptions{
		Kind:        ui.FileDialogSaveFile,
		DefaultName: "trace-subscriptions.json",
	})
	if err == nil || got != "" {
		t.Fatalf("SaveFile error expected non-nil err, got (%q, %v)", got, err)
	}
	if gotSave.DefaultPath != "trace-subscriptions.json" {
		t.Fatalf("SaveFile empty DefaultPath resolved to %q, want %q", gotSave.DefaultPath, "trace-subscriptions.json")
	}

	// SaveFile with nonexistent parent directory + explicit file name preserves the cleaned path.
	nonexistentFile := filepath.Join(tmpDir, "missing-dir", "custom-subs.json")
	saveReturn, saveErr = nonexistentFile, nil
	got, err = handler(ui.FileDialogOptions{
		Kind:        ui.FileDialogSaveFile,
		DefaultPath: nonexistentFile,
		DefaultName: "trace-subscriptions.json",
	})
	if err != nil || got != nonexistentFile {
		t.Fatalf("SaveFile nonexistent parent got (%q, %v)", got, err)
	}
	if gotSave.DefaultPath != nonexistentFile {
		t.Fatalf("SaveFile nonexistent parent DefaultPath = %q, want %q", gotSave.DefaultPath, nonexistentFile)
	}
}
