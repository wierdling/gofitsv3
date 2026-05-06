package debuglog

import (
	"fmt"
	"sync"
	"time"
)

var mu sync.Mutex
var handler func(string)

// SetHandler registers f to be called with each log message.
// Passing nil disables logging. Safe to call from any goroutine.
func SetHandler(f func(string)) {
	mu.Lock()
	defer mu.Unlock()
	handler = f
}

// Log emits a timestamped message to the registered handler.
// No-op if no handler is set.
func Log(msg string) {
	mu.Lock()
	h := handler
	mu.Unlock()
	if h == nil {
		return
	}
	ts := time.Now().Format("15:04:05.000")
	h(fmt.Sprintf("[%s] %s", ts, msg))
}
