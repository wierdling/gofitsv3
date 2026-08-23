package debuglog

import (
	"strings"
	"sync"
	"testing"
)

func TestHandlerLifecycleAndReentrancy(t *testing.T) {
	SetHandler(nil)
	Log("ignored")
	var mu sync.Mutex
	var lines []string
	SetHandler(func(line string) { mu.Lock(); lines = append(lines, line); mu.Unlock() })
	Log("first")
	SetHandler(func(line string) {
		if !strings.Contains(line, "second") {
			t.Errorf("replacement handler received %q", line)
		}
	})
	Log("second")
	SetHandler(nil)
	Log("ignored again")
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || !strings.Contains(lines[0], "] first") {
		t.Fatalf("captured lines = %#v", lines)
	}
}

func TestHandlerCanReplaceItself(t *testing.T) {
	done := make(chan struct{})
	SetHandler(func(string) { SetHandler(nil); close(done) })
	Log("reentrant")
	<-done
}
