package main

import (
	"log"

	// The Chinese catalog makes MyGo's own strings (menus, dialogs, text
	// fields) and the installer Chinese.
	_ "github.com/chialecode/trace/internal/locales"
	"github.com/chialecode/trace/internal/shell"
)

func main() {
	if err := shell.Run(); err != nil {
		log.Fatal(err)
	}
}
