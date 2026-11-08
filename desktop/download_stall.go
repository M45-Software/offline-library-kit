package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

const defaultStallSeconds = 60
const minStallSeconds = 15
const maxStallSeconds = 600

func (a *app) effectiveStallSeconds() int {
	if a.stallSeconds == 0 {
		return defaultStallSeconds
	}
	return a.stallSeconds
}
func (a *app) effectiveStallTimeout() time.Duration {
	if a.stallTimeout > 0 {
		return a.stallTimeout
	}
	return time.Duration(a.effectiveStallSeconds()) * time.Second
}

// Count only time spent waiting for the network, excluding hashing and disk I/O.
// A generation prevents a late timer callback from cancelling a healthy read.
type downloadIdleGuard struct {
	mu              sync.Mutex
	parent          context.Context
	cancel          context.CancelFunc
	timeout         time.Duration
	timer           *time.Timer
	generation      uint64
	active, stalled bool
}

func (g *downloadIdleGuard) begin() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active {
		return
	}
	g.active = true
	g.generation++
	generation := g.generation
	g.timer = time.AfterFunc(g.timeout, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.active && g.generation == generation {
			g.stalled = true
			g.cancel()
		}
	})
}
func (g *downloadIdleGuard) end(err error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active = false
	if g.timer != nil {
		g.timer.Stop()
	}
	if g.parent.Err() != nil {
		return g.parent.Err()
	}
	if g.stalled {
		return &transientDownloadError{err: fmt.Errorf("No download data received for %s; connection stalled", g.timeout)}
	}
	return err
}
func (g *downloadIdleGuard) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active = false
	if g.timer != nil {
		g.timer.Stop()
	}
	g.cancel()
}
func (g *downloadIdleGuard) read(reader io.Reader, buffer []byte) (int, error) {
	g.begin()
	n, err := reader.Read(buffer)
	if n > 0 || err != nil {
		return n, g.end(err)
	}
	g.mu.Lock()
	stalled := g.stalled
	g.mu.Unlock()
	if stalled {
		return n, g.end(err)
	}
	return n, nil
}
