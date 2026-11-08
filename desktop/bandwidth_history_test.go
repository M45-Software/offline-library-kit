package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentArchiveTransfersShareCapWithoutFalseStalls(t *testing.T) {
	payload := bytes.Repeat([]byte("cap"), 21845)
	hash := sha256.Sum256(payload)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write(payload) }))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), downloadConcurrency: 2, bandwidthKiB: 64, stallTimeout: 200 * time.Millisecond}
	seedParallelQueue(t, a, parallelItems(2))
	defer stopFailureTestQueue(t, a)
	start := time.Now()
	if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
		return downloadRequest{URL: server.URL, Name: id + ".zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:])}, nil
	}); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, a)
	if elapsed := time.Since(start); elapsed < 1800*time.Millisecond {
		t.Fatalf("workers exceeded total cap: %s", elapsed)
	}
	if requests.Load() != 2 || a.current.State != "complete" || len(a.downloadHistory) != 2 {
		t.Fatalf("pacing caused a stall/retry: %d %+v", requests.Load(), a.current)
	}
}

func TestBandwidthLimitIsSharedAndChangesWakeWaiters(t *testing.T) {
	l := newDownloadLimiter(128)
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			if err := l.wait(context.Background(), 64*1024); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("shared cap not enforced: %s", elapsed)
	}
	l.set(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- l.wait(ctx, 64*1024) }()
	l.set(0)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unlimited change did not wake limiter")
	}
	l.set(1)
	cancel()
	if err := l.wait(ctx, 64*1024); !errors.Is(err, context.Canceled) {
		t.Fatal("pause ignored")
	}
}

func TestLiveBandwidthSettingPersistsAndRollsBackOnConflict(t *testing.T) {
	a := &app{root: t.TempDir(), token: "secret"}
	seedParallelQueue(t, a, parallelItems(1))
	started := make(chan struct{})
	if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
		close(started)
		<-ctx.Done()
		return downloadRequest{}, ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	defer stopFailureTestQueue(t, a)
	<-started
	if response := queueAPICall(a, "/api/download/bandwidth", map[string]float64{"mibPerSecond": .5}); response.Code != 200 {
		t.Fatal(response.Body)
	}
	restored := &app{root: a.root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if restored.bandwidthKiB != 512 || !a.bandwidth.limited() {
		t.Fatal("live setting not persisted/applied")
	}
	for _, value := range []float64{-1, 1025, .0001} {
		if response := queueAPICall(a, "/api/download/bandwidth", map[string]float64{"mibPerSecond": value}); response.Code != 400 {
			t.Fatal("invalid cap accepted")
		}
	}
	path := filepath.Join(a.root, queueFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if response := queueAPICall(a, "/api/download/bandwidth", map[string]float64{"mibPerSecond": 0}); response.Code != 409 {
		t.Fatal("overwrote conflicting settings")
	}
	if a.bandwidthKiB != 512 || !a.bandwidth.limited() {
		t.Fatal("failed save changed effective limiter")
	}
}

func TestHistoryPersistsBoundsAndRedactsPublisherURLs(t *testing.T) {
	a := &app{root: t.TempDir()}
	seedParallelQueue(t, a, nil)
	cause := &url.Error{Op: "Get", URL: "https://user:secret@example.com/archive?token=private", Err: &httpDownloadError{status: 503}}
	for i := 0; i < 70; i++ {
		a.appendHistoryLocked(queueID1, fmt.Sprintf("Item %d", i), cause, 3, 503)
	}
	if err := a.saveQueueLocked(nil, nil); err != nil {
		t.Fatal(err)
	}
	restored := &app{root: a.root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if len(restored.downloadHistory) != 50 || restored.downloadHistory[0].Title != "Item 69" || restored.downloadHistory[0].Retries != 3 || restored.downloadHistory[0].HTTPStatus != 503 {
		t.Fatal("history not retained/bounded")
	}
	data, err := os.ReadFile(filepath.Join(a.root, queueFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private", "secret", "example.com"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("publisher URL leaked to history")
		}
	}
	if detail := historyDetail(errors.New("failed https://example.com/x?password=private\nnext")); strings.Contains(detail, "private") || strings.Contains(detail, "\n") {
		t.Fatal(detail)
	}
}

func TestQueueHistoryKeepsSuccessAndFailureDetails(t *testing.T) {
	t.Parallel()
	a := &app{root: t.TempDir(), client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: http.NoBody}, nil
	})}}
	seedParallelQueue(t, a, parallelItems(1))
	var attempts int
	last := make(chan struct{})
	if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
		attempts++
		if attempts == 4 {
			close(last)
		}
		return downloadRequest{}, responseDownloadError(&http.Response{StatusCode: 503, Header: make(http.Header)})
	}); err != nil {
		t.Fatal(err)
	}
	defer stopFailureTestQueue(t, a)
	select {
	case <-last:
	case <-time.After(10 * time.Second):
		t.Fatal("retry timeout")
	}
	waitQueue(t, a)
	if len(a.downloadHistory) != 1 || a.downloadHistory[0].Retries != 3 || a.downloadHistory[0].HTTPStatus != 503 || a.downloadHistory[0].State != "failed" {
		t.Fatalf("missing failure diagnostics: %+v", a.downloadHistory)
	}
	a.mu.Lock()
	a.appendHistoryLocked(queueID2, "Finished", nil, 0, 0)
	err := a.saveQueueLocked(nil, a.catalogQueue)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	restored := &app{root: a.root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if len(restored.downloadHistory) != 2 || restored.downloadHistory[0].State != "complete" || !restored.catalogQueue[0].NetworkFailure {
		t.Fatal("terminal history lost queue failure")
	}
}

func TestSpaceReportSeparatesReservationsAndEstimatedRemaining(t *testing.T) {
	a, b := &app{queueID: "a"}, &app{queueID: "b"}
	budget := newQueueSpaceBudget(t.TempDir(), 2)
	budget.reservations[a] = spaceReservation{remaining: 100, extra: 200}
	budget.reservations[b] = spaceReservation{remaining: 0, extra: 300}
	report := queueSpaceSnapshot(budget, []queuedCollection{{ID: "c", ApproxBytes: 1000}, {ID: "unknown"}}, []map[string]any{{"id": "a"}, {"id": "b"}}, []savedPartial{{CatalogID: "c", Done: 250, CanResume: true}}, job{}, nil)
	if report.ReservedDownloadBytes != 100 || report.ReservedPreparationBytes != 500 || report.RemainingDownloadBytes != 850 || !report.RemainingUnknown {
		t.Fatalf("double counted progress/reservations: %+v", report)
	}
	report = queueSpaceSnapshot(nil, nil, nil, nil, job{State: "verifying"}, &downloadRequest{Bytes: 1000})
	if report.ReservedDownloadBytes != 0 || report.RemainingDownloadBytes != 0 {
		t.Fatal("verification counted as remaining transfer")
	}
}

func TestManualDownloadHistoryKeepsVerifiedOutcomeAndHTTPFailure(t *testing.T) {
	for _, status := range []int{200, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			payload := []byte("manual history")
			hash := sha256.Sum256(payload)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				if status == 200 {
					w.Write(payload)
				}
			}))
			defer server.Close()
			a := &app{root: t.TempDir(), client: server.Client()}
			seedParallelQueue(t, a, nil)
			item := downloadRequest{Name: "manual.zim", URL: server.URL + "/?token=private-fixture", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:])}
			if !a.beginDownload(job{State: "resolving", Name: "Manual"}, func(context.Context) (downloadRequest, error) { return item, nil }) {
				t.Fatal("did not start")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				a.mu.Lock()
				active := activeDownload(a.current.State)
				a.mu.Unlock()
				if !active {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("manual download did not stop")
				}
				time.Sleep(5 * time.Millisecond)
			}
			restored := &app{root: a.root}
			if err := restored.initializeQueueStore(); err != nil {
				t.Fatal(err)
			}
			if len(restored.downloadHistory) != 1 {
				t.Fatal("manual outcome missing")
			}
			entry := restored.downloadHistory[0]
			if entry.Title != "manual.zim" || entry.Retries != 0 || strings.Contains(entry.Detail, "private-fixture") {
				t.Fatalf("bad history: %+v", entry)
			}
			if status == 200 && entry.State != "complete" || status == 404 && (entry.State != "failed" || entry.HTTPStatus != 404) {
				t.Fatalf("wrong result: %+v", entry)
			}
		})
	}
}
