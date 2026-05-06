package debugtime

import (
	"log"
	"time"
)

func Track(name string) func() {
	start := time.Now()
	return func() {
		log.Printf("%s took %v", name, time.Since(start))
	}
}
