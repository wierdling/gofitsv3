package ui

import (
	"context"
	"sync"
)

// jobGeneration lets a newer job of one kind supersede older ones. begin
// cancels the previous job's context and hands out a fresh generation;
// a worker publishes its result only while that generation is still current
// and its context is live. Safe for concurrent use.
type jobGeneration struct {
	mu     sync.Mutex
	gen    uint64
	cancel context.CancelFunc
}

// begin cancels any in-flight job and starts a new one derived from parent
// (context.Background when nil).
func (g *jobGeneration) begin(parent context.Context) (context.Context, uint64) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
	}
	g.cancel = cancel
	g.gen++
	gen := g.gen
	g.mu.Unlock()
	return ctx, gen
}

// current reports whether gen is still the latest generation.
func (g *jobGeneration) current(gen uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gen == gen
}

// invalidate cancels the in-flight job and makes every outstanding
// generation stale, for example when the workspace is reset.
func (g *jobGeneration) invalidate() {
	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
		g.cancel = nil
	}
	g.gen++
	g.mu.Unlock()
}

// publishIfCurrent runs apply only when gen is current and ctx has not been
// cancelled, and reports whether it ran. Call it on the UI goroutine (inside
// fyne.Do) so the check and the apply cannot interleave with a newer begin.
func (g *jobGeneration) publishIfCurrent(ctx context.Context, gen uint64, apply func()) bool {
	if ctx.Err() != nil || !g.current(gen) {
		return false
	}
	apply()
	return true
}
