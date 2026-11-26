package main

import (
	"log"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/app"
	_ "github.com/egoist/mygo/plugins/camera"
	_ "github.com/egoist/mygo/plugins/permissionflow"
	_ "github.com/egoist/mygo/plugins/preferences"
	_ "github.com/egoist/mygo/plugins/videoplayer"
)

// Build-time variables injected by build scripts (-ldflags -X).
var (
	channel = ""
	version = ""
)

func main() {
	a := app.New(app.Options{
		Channel:    channel,
		AppVersion: version,
	})
	if err := a.Run(); err != nil {
		log.Fatal(err)
	}
}
