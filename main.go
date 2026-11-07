package main

import (
	"log"

	"github.com/chialecode/trace/internal/shell"
)

func main() {
	if err := shell.Run(); err != nil {
		log.Fatal(err)
	}
}
