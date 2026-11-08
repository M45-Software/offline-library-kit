package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestDownloadPublishesMeteredProgressBeforeVerification(t *testing.T) {
	payload := []byte("metered transfer fixture")
	hash := sha256.Sum256(payload)
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.Write(payload[:4])
		w.(http.Flusher).Flush()
		select {
		case <-release:
			w.Write(payload[4:])
		case <-r.Context().Done():
		}
	}))
	defer func() { once.Do(func() { close(release) }); server.Close() }()
	a := &app{root: t.TempDir(), client: server.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- a.download(ctx, downloadRequest{URL: server.URL, Name: "metered.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:])})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.mu.Lock()
		j := a.current.withTransferProgress(time.Now())
		a.mu.Unlock()
		if j.State == "downloading" && j.SpeedMeasured && j.BytesPerSecond > 0 && j.ETASeconds != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("download did not publish metered progress: %+v", j)
		}
		time.Sleep(10 * time.Millisecond)
	}
	once.Do(func() { close(release) })
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	j := a.current.withTransferProgress(time.Now())
	a.mu.Unlock()
	if j.State != "verifying" || j.SpeedMeasured || j.ETASeconds != nil {
		t.Fatalf("verification kept a transfer ETA: %+v", j)
	}
}

func TestTransferRateExcludesResumedBytesAndWaitsForBaseline(t *testing.T) {
	start := time.Now()
	const offset int64 = 1_000_000_000
	m := newTransferMeter(start, offset)
	m.record(start.Add(500*time.Millisecond), offset+500)
	if rate, eta, ready := m.snapshot(start.Add(500*time.Millisecond), offset+10_000); rate != 0 || eta != nil || ready {
		t.Fatal("showed speed before a useful baseline")
	}
	m.record(start.Add(2*time.Second), offset+2000)
	rate, eta, ready := m.snapshot(start.Add(2*time.Second), offset+10_000)
	if rate != 1000 || eta == nil || *eta != 8 || !ready {
		t.Fatalf("resumed bytes inflated progress: %v %v %v", rate, eta, ready)
	}
}

func TestTransferRateUsesBoundedRecentWindow(t *testing.T) {
	start := time.Now()
	m := newTransferMeter(start, 0)
	for i := 1; i <= 400; i++ {
		done := int64(i * 25)
		if i > 200 {
			done = 5000 + int64(i-200)*75
		}
		m.record(start.Add(time.Duration(i)*50*time.Millisecond), done)
	}
	rate, _, _ := m.snapshot(start.Add(20*time.Second), 30_000)
	if rate != 1500 {
		t.Fatalf("old throughput leaked into recent rate: %v", rate)
	}
	if len(m.samples) > 42 {
		t.Fatalf("unbounded samples: %d", len(m.samples))
	}
	// The cutoff lies between samples; interpolate rather than count a whole chunk.
	m.record(start.Add(20100*time.Millisecond), 20_150)
	rate, _, _ = m.snapshot(start.Add(20100*time.Millisecond), 30_000)
	if math.Abs(rate-1500) > .001 {
		t.Fatalf("window boundary skewed rate: %v", rate)
	}
}

func TestTransferRateDecaysDuringStallAndResetsOnReconnect(t *testing.T) {
	start := time.Now()
	m := newTransferMeter(start, 0)
	m.record(start.Add(2*time.Second), 2000)
	rate, eta, ready := m.snapshot(start.Add(5*time.Second), 4000)
	if rate != 400 || eta == nil || *eta != 5 || !ready {
		t.Fatalf("idle time excluded: %v %v", rate, eta)
	}
	rate, eta, ready = m.snapshot(start.Add(12*time.Second), 4000)
	if rate != 0 || eta != nil || !ready {
		t.Fatal("stale speed and ETA survived a stall")
	}
	reconnected := newTransferMeter(start.Add(20*time.Second), 2000)
	reconnected.record(start.Add(22*time.Second), 2200)
	rate, eta, _ = reconnected.snapshot(start.Add(22*time.Second), 4000)
	if rate != 100 || eta == nil || *eta != 18 {
		t.Fatalf("reconnect reused old speed: %v %v", rate, eta)
	}
}

func TestTransferStatsAreClearedOutsideDownloading(t *testing.T) {
	start := time.Now().Add(-2 * time.Second)
	m := newTransferMeter(start, 0)
	m.record(start.Add(time.Second), 1000)
	for _, state := range []string{"checking", "verifying", "indexing", "repairing", "waiting", "retrying", "paused", "canceled", "failed", "complete"} {
		eta := int64(10)
		got := (job{State: state, Total: 2000, transfer: m, BytesPerSecond: 123, ETASeconds: &eta, SpeedMeasured: true}).withTransferProgress(time.Now())
		if got.BytesPerSecond != 0 || got.ETASeconds != nil || got.SpeedMeasured {
			t.Fatalf("%s retained download stats", state)
		}
	}
}

func TestStateExportsLiveTransferStatsForSingleAndQueuedDownloads(t *testing.T) {
	start := time.Now().Add(-2 * time.Second)
	m := newTransferMeter(start, 5000)
	m.record(start.Add(time.Second), 6000)
	j := job{State: "downloading", Name: "test.zim", Done: 6000, Total: 10_000, transfer: m}
	a := &app{root: t.TempDir(), current: j, queueInFlight: parallelItems(1), queueJobs: map[string]job{parallelItems(1)[0].ID: j}}
	response := httptest.NewRecorder()
	a.handler().ServeHTTP(response, httptest.NewRequest("GET", "http://127.0.0.1:9999/api/state", nil))
	var state struct {
		Job     job `json:"job"`
		Running []struct {
			Job job `json:"job"`
		} `json:"queueRunning"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Running) != 1 {
		t.Fatal("missing running job")
	}
	for _, got := range []job{state.Job, state.Running[0].Job} {
		if !got.SpeedMeasured || got.BytesPerSecond < 400 || got.BytesPerSecond > 600 || got.ETASeconds == nil || *got.ETASeconds < 8 {
			t.Fatalf("bad live stats: %+v", got)
		}
	}
}

func TestTransferMeterSupportsConcurrentProgressAndPolling(t *testing.T) {
	start := time.Now()
	m := newTransferMeter(start, 0)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 1; i <= 1000; i++ {
			m.record(start.Add(time.Duration(i)*time.Millisecond), int64(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 1; i <= 1000; i++ {
			m.snapshot(start.Add(time.Duration(i)*time.Millisecond), 2000)
		}
	}()
	wg.Wait()
}
