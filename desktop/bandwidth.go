package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sync"
	"time"
)

const maxBandwidthKiB = 1024 * 1024
const bandwidthChunk = 64 * 1024

// One bucket is shared by all library workers. Waiting never holds app.mu or
// counts toward the network stall timer. At most one small body chunk per worker
// can be buffered ahead of the limiter; it is not an OS-level network shaper.
type downloadLimiter struct {
	mu      sync.Mutex
	rate    float64
	tokens  float64
	updated time.Time
	changed chan struct{}
}

func newDownloadLimiter(kib int) *downloadLimiter {
	return &downloadLimiter{rate: float64(kib) * 1024, updated: time.Now(), changed: make(chan struct{})}
}
func (l *downloadLimiter) set(kib int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rate = float64(kib) * 1024
	l.tokens = 0
	l.updated = time.Now()
	close(l.changed)
	l.changed = make(chan struct{})
}
func (l *downloadLimiter) limited() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.rate > 0 }
func (l *downloadLimiter) wait(ctx context.Context, bytes int) error {
	for bytes > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.Lock()
		if l.rate == 0 {
			l.mu.Unlock()
			return nil
		}
		n := min(bytes, bandwidthChunk, int(math.Max(1, l.rate/10)))
		now := time.Now()
		l.tokens = math.Min(bandwidthChunk, l.tokens+now.Sub(l.updated).Seconds()*l.rate)
		l.updated = now
		if l.tokens >= float64(n) {
			l.tokens -= float64(n)
			l.mu.Unlock()
			bytes -= n
			continue
		}
		delay := time.Duration(math.Ceil((float64(n) - l.tokens) / l.rate * float64(time.Second)))
		changed := l.changed
		l.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

// Caller holds app.mu.
func (a *app) limiterLocked() *downloadLimiter {
	if a.bandwidth == nil {
		a.bandwidth = newDownloadLimiter(a.bandwidthKiB)
	}
	return a.bandwidth
}
func (a *app) addBandwidthHandler(mux *http.ServeMux) {
	mux.HandleFunc("/api/download/bandwidth", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MiB *float64 `json:"mibPerSecond"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request); err != nil || request.MiB == nil || math.IsNaN(*request.MiB) || math.IsInf(*request.MiB, 0) || *request.MiB < 0 || *request.MiB > 1024 || *request.MiB > 0 && *request.MiB < .001 {
			http.Error(w, "Choose 0 (unlimited) or 0.001 to 1024 MiB/s", 400)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closing {
			http.Error(w, "App is closing", 409)
			return
		}
		previous := a.bandwidthKiB
		a.bandwidthKiB = int(math.Round(*request.MiB * 1024))
		if err := a.saveQueueLocked(a.queueCurrent, a.catalogQueue); err != nil {
			a.bandwidthKiB = previous
			http.Error(w, "Could not save bandwidth limit: "+err.Error(), 409)
			return
		}
		a.limiterLocked().set(a.bandwidthKiB)
		writeJSON(w, map[string]float64{"mibPerSecond": float64(a.bandwidthKiB) / 1024})
	})
}
