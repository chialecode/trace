package ui

import (
	_ "embed"
	"sync"

	"github.com/egoist/mygo/ui"
)

// InterVariable.ttf is Inter 4.1 (https://github.com/rsms/inter, release
// v4.1, Inter-4.1.zip), SIL Open Font License 1.1 (fonts/OFL.txt); see
// fonts/README.md for its source and checksum. One variable font gives the
// weights the theme uses (400, 500, 600).
//
//go:embed fonts/InterVariable.ttf
var interVariable []byte

var (
	fontOnce       sync.Once
	fontRegistered bool
)

// registerUIFont registers Inter once. Where the system cannot add fonts
// (DirectWrite before Windows 10 1703), text keeps the system's font.
func registerUIFont() {
	fontOnce.Do(func() {
		fontRegistered = ui.RegisterFont(interVariable, UIFont) == nil
	})
}
