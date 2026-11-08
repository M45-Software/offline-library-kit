package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestStalledDownloadReconnectsAndResumes(t *testing.T) {
	payload := []byte("stalled download fixture")
	hash := sha256.Sum256(payload)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.Write(payload[:4])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Range") != "bytes=4-" {
			t.Errorf("lost partial on reconnect: %s", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 4-%d/%d", len(payload)-1, len(payload)))
		w.WriteHeader(206)
		w.Write(payload[4:])
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), stallTimeout: 100 * time.Millisecond}
	item := downloadRequest{URL: server.URL, Name: "stalled.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:])}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := a.downloadWithRetries(ctx, item); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(a.root, "content", "zim", item.Name))
	if err != nil || string(actual) != string(payload) || requests.Load() != 2 {
		t.Fatalf("bad reconnect: %q %v requests=%d", actual, err, requests.Load())
	}
}
func TestSlowButActiveDownloadDoesNotStall(t *testing.T) {
	payload := []byte("steady byte stream")
	hash := sha256.Sum256(payload)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		for _, b := range payload {
			w.Write([]byte{b})
			w.(http.Flusher).Flush()
			time.Sleep(15 * time.Millisecond)
		}
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), stallTimeout: 100 * time.Millisecond}
	if err := a.downloadWithRetries(context.Background(), downloadRequest{URL: server.URL, Name: "steady.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:])}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("reconnected despite continuous progress")
	}
}

func TestStalledResponseHeadersReconnect(t *testing.T) {
	payload := []byte("response after reconnect")
	hash := sha256.Sum256(payload)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.Write(payload)
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), stallTimeout: 100 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := a.downloadWithRetries(ctx, downloadRequest{URL: server.URL, Name: "headers.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:])}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("expected one reconnect, got %d requests", requests.Load())
	}
}
func TestIdleTimerStopsDuringDiskWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard := &downloadIdleGuard{parent: ctx, cancel: cancel, timeout: 10 * time.Millisecond}
	defer guard.close()
	guard.begin()
	if err := guard.end(nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("disk/hash time counted as stalled network")
	}
}
func TestStallSettingPersistsAndRejectsInvalidValues(t *testing.T) {
	a := &app{root: t.TempDir(), token: "secret"}
	seedParallelQueue(t, a, nil)
	if a.effectiveStallSeconds() != 60 {
		t.Fatal("wrong default")
	}
	for _, seconds := range []int{0, 14, 601} {
		response := queueAPICall(a, "/api/queue/concurrency", map[string]int{"concurrency": 4, "stallSeconds": seconds})
		if response.Code != 400 {
			t.Fatalf("accepted timeout %d", seconds)
		}
	}
	response := queueAPICall(a, "/api/queue/concurrency", map[string]int{"concurrency": 2, "stallSeconds": 120})
	if response.Code != 200 {
		t.Fatal(response.Body)
	}
	restored := &app{root: a.root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if restored.effectiveStallSeconds() != 120 || restored.effectiveDownloadConcurrency() != 2 {
		t.Fatal("settings not restored together")
	}
}
