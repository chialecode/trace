// Command icons draws the application icons of TRACE: assets/icon.png for
// the release channel (a white "T" on dark gray) and assets/icon-dev.png for
// the development channel (the same mark with an amber "DEV" badge), both
// with internal/brandicon, which the tray icon is drawn with too. Both are
// 1024x1024 PNGs that MyGo turns into the icons of the executable, the
// installer and the uninstall entry.
//
//	go run ./tools/icons
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chialecode/trace/internal/brandicon"
)

func main() {
	out := flag.String("out", "assets", "directory of icon.png and icon-dev.png")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "icons:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for name, dev := range map[string]bool{"icon.png": false, "icon-dev.png": true} {
		data, err := brandicon.PNG(brandicon.Design, brandicon.Options{Badge: dev})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d bytes)\n", filepath.Join(out, name), len(data))
	}
	return nil
}
