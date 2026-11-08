package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func stopFailureTestQueue(t *testing.T, a *app) {
	t.Helper()
	a.mu.Lock()
	cancel := a.cancelDownload
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		waitQueue(t, a)
	}
}

func TestExhaustedDownloadIsIsolatedAndRetryResumesPartial(t *testing.T) {
	t.Parallel()
	items := parallelItems(3)
	payload := []byte("isolated network failure fixture")
	hash := sha256.Sum256(payload)
	var attempts atomic.Int32
	var recovered atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		if r.URL.Path == "/"+items[0].ID {
			attempts.Add(1)
			if !recovered.Load() {
				if r.Header.Get("Range") != "" {
					w.WriteHeader(503)
					return
				}
				w.Write(payload[:4])
				return
			}
			if r.Header.Get("Range") != "bytes=4-" {
				t.Errorf("partial not resumed: %s", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)-4))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 4-%d/%d", len(payload)-1, len(payload)))
			w.WriteHeader(206)
			w.Write(payload[4:])
			return
		}
		w.Write(payload)
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), token: "secret", downloadConcurrency: 2}
	seedParallelQueue(t, a, items)
	defer stopFailureTestQueue(t, a)
	otherStarted := make(chan struct{})
	otherCanceled := make(chan struct{})
	releaseOther := make(chan struct{})
	resolve := func(ctx context.Context, id string) (downloadRequest, error) {
		if id == items[1].ID {
			close(otherStarted)
			select {
			case <-releaseOther:
			case <-ctx.Done():
				close(otherCanceled)
				return downloadRequest{}, ctx.Err()
			}
		}
		return downloadRequest{URL: server.URL + "/" + id, Name: id + ".zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:])}, nil
	}
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal(err)
	}
	select {
	case <-otherStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("other worker did not start")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		a.mu.Lock()
		isolated := len(a.catalogQueue) == 1 && a.catalogQueue[0].ID == items[0].ID && a.catalogQueue[0].NetworkFailure && a.catalogQueue[0].Paused
		a.mu.Unlock()
		_, err := os.Stat(filepath.Join(a.root, "content", "zim", items[2].ID+".zim"))
		if isolated && err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed item was not isolated or next item did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-otherCanceled:
		t.Fatal("network failure canceled another worker")
	default:
	}
	if attempts.Load() != 4 {
		t.Fatalf("attempts=%d", attempts.Load())
	}
	restored := &app{root: a.root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range restored.catalogQueue {
		if item.ID == items[0].ID {
			found = item.NetworkFailure && item.Paused
		}
	}
	if !found {
		t.Fatal("network failure did not survive restart")
	}
	if restored.eligibleQueueIndexLocked(map[string]bool{items[0].ID: true}) >= 0 {
		t.Fatal("failed item eligible for automatic restart")
	}
	recovered.Store(true)
	if response := queueAPICall(a, "/api/queue/resume-item", map[string]string{"id": items[0].ID}); response.Code != 200 {
		t.Fatal(response.Body)
	}
	close(releaseOther)
	waitQueue(t, a)
	if len(a.catalogQueue) != 0 || a.current.State != "complete" {
		t.Fatalf("retry failed: %+v", a.current)
	}
	actual, err := os.ReadFile(filepath.Join(a.root, "content", "zim", items[0].ID+".zim"))
	if err != nil || string(actual) != string(payload) || attempts.Load() != 5 {
		t.Fatalf("retry lost partial: %q %v attempts=%d", actual, err, attempts.Load())
	}
}

func TestExhaustedMetadataRetriesLeaveOnlyFailedItemPaused(t *testing.T) {
	t.Parallel()
	a := &app{root: t.TempDir(), downloadConcurrency: 1}
	items := parallelItems(1)
	seedParallelQueue(t, a, items)
	defer stopFailureTestQueue(t, a)
	finished := make(chan struct{})
	var attempts atomic.Int32
	if err := a.startCatalogQueue(func(context.Context, string) (downloadRequest, error) {
		if attempts.Add(1) == 4 {
			close(finished)
		}
		return downloadRequest{}, &transientDownloadError{err: errors.New("metadata unavailable")}
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("retries did not finish")
	}
	waitQueue(t, a)
	if a.current.State != "paused" || len(a.catalogQueue) != 1 || !a.catalogQueue[0].NetworkFailure {
		t.Fatalf("wrong failure state: %+v %+v", a.current, a.catalogQueue)
	}
	if err := a.startCatalogQueue(a.queueResolve); err == nil {
		t.Fatal("list start automatically retried failed item")
	}
}

func TestStorageAndIntegrityFailuresStillStopOtherWorkers(t *testing.T) {
	for _, cause := range []error{&os.PathError{Op: "write", Path: "partial.zim", Err: syscall.ENOSPC}, errors.New("SHA-256 mismatch")} {
		t.Run(cause.Error(), func(t *testing.T) {
			a := &app{root: t.TempDir(), downloadConcurrency: 2}
			seedParallelQueue(t, a, parallelItems(3))
			defer stopFailureTestQueue(t, a)
			otherStarted := make(chan struct{})
			otherCanceled := make(chan struct{})
			if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
				if id == parallelItems(3)[0].ID {
					<-otherStarted
					return downloadRequest{}, cause
				}
				close(otherStarted)
				<-ctx.Done()
				close(otherCanceled)
				return downloadRequest{}, ctx.Err()
			}); err != nil {
				t.Fatal(err)
			}
			waitQueue(t, a)
			select {
			case <-otherCanceled:
			default:
				t.Fatal("fatal error left another worker running")
			}
			if a.current.State != "failed" || len(a.catalogQueue) != 3 {
				t.Fatalf("fatal failure lost list: %+v", a.current)
			}
			for _, item := range a.catalogQueue {
				if item.NetworkFailure {
					t.Fatal("fatal error isolated as network failure")
				}
			}
		})
	}
}

func TestFailureStateSaveConflictStopsWholeList(t *testing.T) {
	t.Parallel()
	a := &app{root: t.TempDir(), downloadConcurrency: 2}
	seedParallelQueue(t, a, parallelItems(3))
	defer stopFailureTestQueue(t, a)
	otherStarted := make(chan struct{})
	var attempts atomic.Int32
	finished := make(chan struct{})
	if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
		if id != parallelItems(3)[0].ID {
			close(otherStarted)
			<-ctx.Done()
			return downloadRequest{}, ctx.Err()
		}
		<-otherStarted
		if attempts.Add(1) == 4 {
			path := filepath.Join(a.root, queueFilename)
			data, err := os.ReadFile(path)
			if err == nil {
				err = os.WriteFile(path, append(data, ' '), 0600)
			}
			if err != nil {
				t.Error(err)
			}
			close(finished)
		}
		return downloadRequest{}, &transientDownloadError{err: errors.New("connection interrupted")}
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("retries did not finish")
	}
	waitQueue(t, a)
	if a.current.State != "failed" || !strings.Contains(a.current.Error, "could not be saved") || len(a.catalogQueue) != 3 {
		t.Fatalf("save failure did not stop list: %+v %+v", a.current, a.catalogQueue)
	}
}
