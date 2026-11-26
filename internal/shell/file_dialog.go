package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/chialecode/trace/internal/ui"
	"github.com/egoist/mygo"
)

func (s *Shell) newFileDialogHandler(win *mygo.Window, winClosed *atomic.Bool) ui.FileDialogHandler {
	return func(opts ui.FileDialogOptions) (string, error) {
		return s.runFileDialog(win, winClosed, opts)
	}
}

func (s *Shell) runFileDialog(win *mygo.Window, winClosed *atomic.Bool, opts ui.FileDialogOptions) (string, error) {
	s.mu.Lock()
	quiet := s.quiet
	quitting := s.quitting
	openFn := s.openFileDialog
	saveFn := s.saveFileDialog
	s.mu.Unlock()
	if quiet != "" || quitting || (winClosed != nil && winClosed.Load()) {
		return "", nil
	}
	if openFn == nil {
		openFn = mygo.Dialog.Open
	}
	if saveFn == nil {
		saveFn = mygo.Dialog.Save
	}

	filters := make([]mygo.FileFilter, 0, len(opts.Filters))
	for _, f := range opts.Filters {
		filters = append(filters, mygo.FileFilter{
			Name:       f.Name,
			Extensions: append([]string(nil), f.Extensions...),
		})
	}

	switch opts.Kind {
	case ui.FileDialogPickDirectory:
		paths, err := openFn(mygo.OpenDialogOptions{
			Parent:            win,
			Title:             opts.Title,
			DefaultPath:       resolveDirectoryPickerDefaultPath(opts.DefaultPath),
			Directory:         true,
			CreateDirectories: true,
		})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil

	case ui.FileDialogOpenFile:
		paths, err := openFn(mygo.OpenDialogOptions{
			Parent:      win,
			Title:       opts.Title,
			DefaultPath: resolveOpenFilePickerDefaultPath(opts.DefaultPath),
			Filters:     filters,
			Directory:   false,
		})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil

	case ui.FileDialogSaveFile:
		return saveFn(mygo.SaveDialogOptions{
			Parent:            win,
			Title:             opts.Title,
			DefaultPath:       resolveSaveFilePickerDefaultPath(opts.DefaultPath, opts.DefaultName),
			Filters:           filters,
			CreateDirectories: true,
		})

	default:
		return "", fmt.Errorf("unsupported file dialog kind: %q", opts.Kind)
	}
}

func cleanPickerPath(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), `"`)
}

func resolveDirectoryPickerDefaultPath(raw string) string {
	p := cleanPickerPath(raw)
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if st, err := os.Stat(p); err == nil {
		if st.IsDir() {
			return p
		}
		return filepath.Dir(p)
	}
	parent := filepath.Dir(p)
	if parent != p {
		if st, err := os.Stat(parent); err == nil && st.IsDir() {
			return parent
		}
	}
	return ""
}

func resolveOpenFilePickerDefaultPath(raw string) string {
	p := cleanPickerPath(raw)
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	parent := filepath.Dir(p)
	if parent != p {
		if st, err := os.Stat(parent); err == nil && st.IsDir() {
			return parent
		}
	}
	return ""
}

func resolveSaveFilePickerDefaultPath(raw, defaultName string) string {
	p := cleanPickerPath(raw)
	defaultName = strings.TrimSpace(defaultName)
	if p == "" {
		return defaultName
	}
	trailingSep := strings.HasSuffix(p, "/") || strings.HasSuffix(p, `\`)
	cleaned := filepath.Clean(p)
	if trailingSep {
		if defaultName != "" {
			return filepath.Join(cleaned, defaultName)
		}
		return cleaned
	}
	if st, err := os.Stat(cleaned); err == nil && st.IsDir() {
		if defaultName != "" {
			return filepath.Join(cleaned, defaultName)
		}
		return cleaned
	}
	return cleaned
}
