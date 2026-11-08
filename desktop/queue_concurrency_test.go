package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	search "offline-library-kit/desktop/internal/librarysearch"
	"offline-library-kit/desktop/internal/zimfixture"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func parallelItems(count int) []queuedCollection {
	items := make([]queuedCollection, count)
	for i := range items {
		items[i] = queuedCollection{ID: fmt.Sprintf("600c47c6-a792-0f78-8fcc-%012d", i+1), Title: fmt.Sprintf("Collection %d", i+1), ApproxBytes: 16}
	}
	return items
}
func seedParallelQueue(t *testing.T, a *app, items []queuedCollection) {
	t.Helper()
	if err := a.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	a.catalogQueue = items
	a.mu.Lock()
	err := a.saveQueueLocked(nil, items)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}
func TestQueueDefaultsToFourConcurrentDownloads(t *testing.T) {
	payload := []byte("parallel payload")
	hash := sha256.Sum256(payload)
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var active, maximum atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if n <= old || maximum.CompareAndSwap(old, n) {
				break
			}
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.Write(payload[:4])
		w.(http.Flusher).Flush()
		started <- struct{}{}
		select {
		case <-release:
			w.Write(payload[4:])
		case <-r.Context().Done():
		}
	}))
	defer func() { once.Do(func() { close(release) }); server.Close() }()
	a := &app{root: t.TempDir(), client: server.Client(), token: "secret"}
	seedParallelQueue(t, a, parallelItems(6))
	resolve := func(ctx context.Context, id string) (downloadRequest, error) {
		return downloadRequest{URL: server.URL + "/" + id, Name: id + ".zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:]), CatalogID: id}, nil
	}
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("four downloads did not overlap")
		}
	}
	state := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1:9999/api/state", nil)
	req.Header.Set("X-Library-Token", "secret")
	a.handler().ServeHTTP(state, req)
	var snapshot struct {
		Concurrency int                `json:"downloadConcurrency"`
		Running     []queuedCollection `json:"queueRunning"`
	}
	if err := json.Unmarshal(state.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Concurrency != 4 || len(snapshot.Running) != 4 {
		t.Fatalf("wrong running state: %+v", snapshot)
	}
	select {
	case <-started:
		t.Fatal("exceeded concurrency limit")
	default:
	}
	once.Do(func() { close(release) })
	waitQueue(t, a)
	if len(a.downloadHistory) != 6 {
		t.Fatalf("missing completed history: %+v", a.downloadHistory)
	}
	for _, entry := range a.downloadHistory {
		if entry.State != "complete" {
			t.Fatalf("bad completed outcome: %+v", entry)
		}
	}
	if maximum.Load() != 4 {
		t.Fatalf("max requests %d", maximum.Load())
	}
	if len(a.catalogQueue) != 0 || a.current.State != "complete" {
		t.Fatalf("queue incomplete: %+v", a.current)
	}
	for _, item := range parallelItems(6) {
		data, err := os.ReadFile(filepath.Join(a.root, "content", "zim", item.ID+".zim"))
		if err != nil || string(data) != string(payload) {
			t.Fatalf("bad file %s: %q %v", item.ID, data, err)
		}
	}
}
func TestParallelQueuePauseAndEditsKeepEveryInFlightItem(t *testing.T) {
	a := &app{root: t.TempDir(), token: "secret"}
	seedParallelQueue(t, a, parallelItems(6))
	started := make(chan struct{}, 4)
	if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
		started <- struct{}{}
		<-ctx.Done()
		return downloadRequest{}, ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	if response := queueAPICall(a, "/api/queue/concurrency", map[string]int{"concurrency": 2}); response.Code != 409 {
		t.Fatal("changed concurrency during downloads")
	}
	extra := parallelItems(7)[6]
	if response := queueAPICall(a, "/api/queue/add", extra); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if response := queueAPICall(a, "/api/queue/remove", map[string]string{"id": parallelItems(6)[5].ID}); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if response := queueAPICall(a, "/api/queue/remove", map[string]string{"id": parallelItems(6)[0].ID}); response.Code != 409 {
		t.Fatal("removed an in-flight item")
	}
	a.mu.Lock()
	cancel := a.cancelDownload
	a.mu.Unlock()
	cancel()
	waitQueue(t, a)
	restored := &app{root: a.root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if len(restored.catalogQueue) != 6 || len(a.catalogQueue) != 6 || a.current.State != "canceled" {
		t.Fatalf("lost paused items: memory=%d disk=%d state=%s", len(a.catalogQueue), len(restored.catalogQueue), a.current.State)
	}
	for i := 0; i < 4; i++ {
		if restored.catalogQueue[i].ID != parallelItems(6)[i].ID {
			t.Fatal("in-flight order changed")
		}
	}
}
func TestDownloadConcurrencyPersistenceAndValidation(t *testing.T) {
	a := &app{root: t.TempDir(), token: "secret"}
	seedParallelQueue(t, a, nil)
	if a.effectiveDownloadConcurrency() != 4 {
		t.Fatal("default is not four")
	}
	for _, value := range []int{0, 9, -1} {
		if response := queueAPICall(a, "/api/queue/concurrency", map[string]int{"concurrency": value}); response.Code != 400 {
			t.Fatalf("accepted %d", value)
		}
	}
	if response := queueAPICall(a, "/api/queue/concurrency", map[string]int{"concurrency": 2}); response.Code != 200 {
		t.Fatal(response.Body)
	}
	restored := &app{root: a.root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if restored.effectiveDownloadConcurrency() != 2 {
		t.Fatal("setting not restored")
	}
	if response := queueAPICall(a, "/api/queue/concurrency", map[string]int{"concurrency": 4}); response.Code != 200 {
		t.Fatal(response.Body)
	}
	restored = &app{root: a.root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if restored.effectiveDownloadConcurrency() != 4 {
		t.Fatal("default not restored")
	}
}

func TestConcurrentDownloadsSerializeSharedIndexConstruction(t *testing.T) {
	source := filepath.Join(t.TempDir(), "fixture.zim")
	if err := zimfixture.Write(source); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(payload)
	identity, err := search.Identify(source)
	if err != nil {
		t.Fatal(err)
	}
	identity.UUID = "01000000-0000-0000-0000-000000000000"
	identity.Collection = "other"
	assets := t.TempDir()
	for _, kind := range []string{"catalog", "custom"} {
		if err := search.Build(context.Background(), filepath.Join(assets, kind), identity, "Other", func(emit func(search.Document) error) error {
			return emit(search.Document{Path: "test", Title: "Other", Text: "other data"})
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), searchDir: assets}
	seedParallelQueue(t, a, parallelItems(2))
	if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
		return downloadRequest{URL: server.URL, Name: "gutenberg_" + id + ".zim", CatalogID: id, Bytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:])}, nil
	}); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, a)
	if a.current.State != "complete" || len(a.catalogQueue) != 0 {
		t.Fatalf("concurrent preparation failed: %+v", a.current)
	}
	entries, err := os.ReadDir(filepath.Join(a.searchRoot(), "local"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one identity-bound index, got %d", len(entries))
	}
	if err := search.Verify(context.Background(), filepath.Join(a.searchRoot(), "local", entries[0].Name())); err != nil {
		t.Fatal(err)
	}
}
