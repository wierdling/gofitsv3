package ui

import (
	"context"
	"sync"
	"testing"
)

func TestJobGenerationPublishesOnlyLatest(t *testing.T) {
	var g jobGeneration
	ctx1, gen1 := g.begin(nil)
	ctx2, gen2 := g.begin(context.Background())
	if ctx1.Err() == nil {
		t.Fatal("begin did not cancel the superseded job's context")
	}
	if g.publishIfCurrent(ctx1, gen1, func() { t.Fatal("stale generation published") }) {
		t.Fatal("publishIfCurrent reported a stale publish")
	}
	ran := false
	if !g.publishIfCurrent(ctx2, gen2, func() { ran = true }) || !ran {
		t.Fatal("current generation was not published")
	}
}

func TestJobGenerationDropsCancelledAndInvalidated(t *testing.T) {
	var g jobGeneration
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, gen := g.begin(parent)
	cancelParent()
	if g.publishIfCurrent(ctx, gen, func() { t.Fatal("cancelled job published") }) {
		t.Fatal("cancelled job reported published")
	}
	ctx, gen = g.begin(nil)
	g.invalidate()
	if ctx.Err() == nil || g.current(gen) {
		t.Fatal("invalidate left the job live")
	}
	if g.publishIfCurrent(ctx, gen, func() { t.Fatal("invalidated job published") }) {
		t.Fatal("invalidated job reported published")
	}
}

func TestJobGenerationConcurrentBegin(t *testing.T) {
	var g jobGeneration
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, gen := g.begin(nil)
			_ = g.current(gen)
			_ = ctx.Err()
		}()
	}
	wg.Wait()
	ctx, gen := g.begin(nil)
	if !g.publishIfCurrent(ctx, gen, func() {}) {
		t.Fatal("latest generation not current after concurrent begins")
	}
}
