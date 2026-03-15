package main

import (
	"log"

	"gofitsv3/internal/ui"
)

func main() {
	if err := ui.Run(); err != nil {
		log.Fatalf("app exited: %v", err)
	}
}
