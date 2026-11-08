package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const queueID1 = "600c47c6-a792-0f78-8fcc-9c05cf1c556b"
const queueID2 = "ab7c8918-9940-f83d-9a14-7f64b4f55636"

func initializeTestQueue(t *testing.T, a *app) {
	t.Helper()
	if a.downloadConcurrency == 0 {
		a.downloadConcurrency = 1
	}
	if err := a.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if len(a.catalogQueue) > 0 {
		a.mu.Lock()
		err := a.saveQueueLocked(nil, a.catalogQueue)
		a.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func waitQueue(t *testing.T, a *app) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		active := a.queueActive
		a.mu.Unlock()
		if !active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("queue did not stop")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestQueueDownloadsSerially(t *testing.T) {
	payload := []byte("verified queue fixture")
	digest := sha256.Sum256(payload)
	var mu sync.Mutex
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), catalogQueue: []queuedCollection{{ID: queueID1, Title: "First"}, {ID: queueID2, Title: "Second"}}}
	initializeTestQueue(t, a)
	resolve := func(ctx context.Context, id string) (downloadRequest, error) {
		name := "one.zim"
		if id == queueID2 {
			name = "two.zim"
		}
		return downloadRequest{URL: server.URL + "/" + name, Name: name, Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), CatalogID: id}, nil
	}
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal("queue did not start")
	}
	waitQueue(t, a)
	a.mu.Lock()
	state, remaining := a.current.State, len(a.catalogQueue)
	a.mu.Unlock()
	if state != "complete" || remaining != 0 {
		t.Fatalf("queue state %s, remaining %d", state, remaining)
	}
	for _, name := range []string{"one.zim", "two.zim"} {
		b, err := os.ReadFile(filepath.Join(a.root, "content", "zim", name))
		if err != nil || string(b) != string(payload) {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(paths) != "[/one.zim /two.zim]" {
		t.Fatalf("wrong download order: %v", paths)
	}
}

func TestQueueFailureRetainsRemainingAndCanRestart(t *testing.T) {
	payload := []byte("queue retry")
	digest := sha256.Sum256(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), catalogQueue: []queuedCollection{{ID: queueID1, Title: "First"}, {ID: queueID2, Title: "Second"}}}
	initializeTestQueue(t, a)
	fail := true
	resolve := func(ctx context.Context, id string) (downloadRequest, error) {
		if fail {
			return downloadRequest{}, errors.New("temporary lookup error")
		}
		name := id + ".zim"
		return downloadRequest{URL: server.URL + "/" + name, Name: name, Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), CatalogID: id}, nil
	}
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal("queue did not start")
	}
	waitQueue(t, a)
	a.mu.Lock()
	state, queued, msg := a.current.State, append([]queuedCollection{}, a.catalogQueue...), a.current.Error
	a.mu.Unlock()
	if state != "failed" || len(queued) != 2 || queued[0].ID != queueID1 || !strings.Contains(msg, "paused") {
		t.Fatalf("failure lost queue: %s %+v %s", state, queued, msg)
	}
	fail = false
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal("queue did not restart")
	}
	waitQueue(t, a)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current.State != "complete" || len(a.catalogQueue) != 0 {
		t.Fatalf("retry incomplete: %+v %+v", a.current, a.catalogQueue)
	}
}

func TestQueueCancelRetainsCurrentAndPreventsConcurrentStart(t *testing.T) {
	a := &app{root: t.TempDir(), token: "secret", catalogQueue: []queuedCollection{{ID: queueID1, Title: "First"}, {ID: queueID2, Title: "Second"}}}
	initializeTestQueue(t, a)
	started := make(chan struct{})
	resolve := func(ctx context.Context, id string) (downloadRequest, error) {
		close(started)
		<-ctx.Done()
		return downloadRequest{}, ctx.Err()
	}
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal("queue did not start")
	}
	<-started
	if a.beginDownload(job{State: "checking"}, func(context.Context) (downloadRequest, error) { return downloadRequest{}, nil }) {
		t.Fatal("concurrent download accepted")
	}
	quit := httptest.NewRequest("POST", "http://127.0.0.1:9999/api/quit", nil)
	quit.Header.Set("X-Library-Token", "secret")
	response := httptest.NewRecorder()
	a.handler().ServeHTTP(response, quit)
	if response.Code != http.StatusConflict {
		t.Fatalf("quit accepted while queue active: %d", response.Code)
	}
	a.mu.Lock()
	cancel := a.cancelDownload
	a.mu.Unlock()
	cancel()
	waitQueue(t, a)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current.State != "canceled" || len(a.catalogQueue) != 2 || a.catalogQueue[0].ID != queueID1 {
		t.Fatalf("cancel lost list: %+v %+v", a.current, a.catalogQueue)
	}
}

func TestQueueAPIResolvesAndDownloadsCatalogItems(t *testing.T) {
	payload := []byte("catalog queue fixture")
	digest := sha256.Sum256(payload)
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		name, id := "one.zim", queueID1
		if strings.Contains(r.URL.Path, queueID2) || strings.Contains(r.URL.Path, "two.zim") {
			name, id = "two.zim", queueID2
		}
		switch {
		case r.URL.Host == "library.kiwix.org":
			entry := fmt.Sprintf(`<entry><id>urn:uuid:%s</id><link rel="http://opds-spec.org/acquisition/open-access" type="application/x-zim" href="https://download.kiwix.org/zim/test/%s.meta4"/></entry>`, id, name)
			return response(entry), nil
		case strings.HasSuffix(r.URL.Path, ".meta4"):
			meta := fmt.Sprintf(`<metalink><file name="%s"><size>%d</size><hash type="sha-256">%s</hash></file></metalink>`, name, len(payload), hex.EncodeToString(digest[:]))
			return response(meta), nil
		default:
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(payload))}, nil
		}
	})}
	a := &app{root: t.TempDir(), client: client, token: "secret"}
	initializeTestQueue(t, a)
	call := func(endpoint string, data any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(data)
		r := httptest.NewRequest("POST", "http://127.0.0.1:9999"+endpoint, bytes.NewReader(b))
		r.Header.Set("X-Library-Token", "secret")
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		return w
	}
	for _, item := range []queuedCollection{{ID: queueID1, Title: "First"}, {ID: queueID2, Title: "Second"}} {
		if w := call("/api/queue/add", item); w.Code != 200 {
			t.Fatalf("add: %d %s", w.Code, w.Body)
		}
	}
	if w := call("/api/queue/add", queuedCollection{ID: queueID1, Title: "Duplicate"}); w.Code != 409 {
		t.Fatalf("duplicate accepted: %d", w.Code)
	}
	if w := call("/api/queue/start", struct{}{}); w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	waitQueue(t, a)
	a.mu.Lock()
	state, remaining := a.current.State, len(a.catalogQueue)
	a.mu.Unlock()
	if state != "complete" || remaining != 0 {
		t.Fatalf("queue incomplete: %s %d", state, remaining)
	}
	for _, name := range []string{"one.zim", "two.zim"} {
		b, err := os.ReadFile(filepath.Join(a.root, "content", "zim", name))
		if err != nil || !bytes.Equal(b, payload) {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
}

func TestQueueCancelDuringDownloadRetainsPartial(t *testing.T) {
	payload := []byte("interrupted queue fixture")
	digest := sha256.Sum256(payload)
	streaming := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload[:3])
		w.(http.Flusher).Flush()
		close(streaming)
		<-r.Context().Done()
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), catalogQueue: []queuedCollection{{ID: queueID1, Title: "First"}, {ID: queueID2, Title: "Second"}}}
	initializeTestQueue(t, a)
	resolve := func(ctx context.Context, id string) (downloadRequest, error) {
		return downloadRequest{URL: server.URL, Name: "interrupted.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), CatalogID: id}, nil
	}
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal("queue did not start")
	}
	select {
	case <-streaming:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	part := filepath.Join(a.root, "content", "zim", "interrupted.zim.part")
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := os.Stat(part)
		if err == nil && info.Size() == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("partial was not written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.mu.Lock()
	cancel := a.cancelDownload
	a.mu.Unlock()
	cancel()
	waitQueue(t, a)
	data, err := os.ReadFile(part)
	if err != nil || !bytes.Equal(data, payload[:3]) {
		t.Fatalf("partial changed: %q %v", data, err)
	}
	if _, err := os.Stat(part + ".json"); err != nil {
		t.Fatalf("resume metadata missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.root, "content", "zim", "interrupted.zim")); !os.IsNotExist(err) {
		t.Fatal("partial installed as final")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current.State != "canceled" || len(a.catalogQueue) != 2 || a.catalogQueue[0].ID != queueID1 {
		t.Fatalf("queue lost after cancellation: %+v %+v", a.current, a.catalogQueue)
	}
}

func TestQueueRecognizesMatchingInstalledArchive(t *testing.T) {
	payload := []byte("already verified archive")
	digest := sha256.Sum256(payload)
	var requests atomic.Int32
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected download")
	})}
	a := &app{root: t.TempDir(), token: "secret", client: client}
	initializeTestQueue(t, a)
	dir := filepath.Join(a.root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "existing.zim")
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if w := queueAPICall(a, "/api/queue/add", queuedCollection{ID: queueID1, Title: "Existing"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	item := downloadRequest{URL: "https://example.invalid/existing.zim", Name: "existing.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), CatalogID: queueID1}
	if err := a.startCatalogQueue(func(context.Context, string) (downloadRequest, error) { return item, nil }); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, a)
	a.mu.Lock()
	state, remaining := a.current.State, len(a.catalogQueue)
	a.mu.Unlock()
	data, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(data, payload) || state != "complete" || remaining != 0 || requests.Load() != 0 {
		t.Fatalf("existing archive not safely recognized: %q %v %s %d %d", data, err, state, remaining, requests.Load())
	}
	restored := &app{root: a.root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if len(restored.catalogQueue) != 0 {
		t.Fatalf("completed item persisted: %+v", restored.catalogQueue)
	}
}

func TestQueuePreservesMismatchedExistingArchive(t *testing.T) {
	existing := []byte("different existing bytes")
	expected := []byte("publisher archive bytes!")
	if len(existing) != len(expected) {
		t.Fatal("test requires same-sized files")
	}
	digest := sha256.Sum256(expected)
	a := &app{root: t.TempDir(), token: "secret"}
	initializeTestQueue(t, a)
	dir := filepath.Join(a.root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "existing.zim")
	if err := os.WriteFile(file, existing, 0600); err != nil {
		t.Fatal(err)
	}
	if w := queueAPICall(a, "/api/queue/add", queuedCollection{ID: queueID1, Title: "Existing"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	item := downloadRequest{URL: "https://example.invalid/existing.zim", Name: "existing.zim", Bytes: int64(len(expected)), SHA256: hex.EncodeToString(digest[:]), CatalogID: queueID1}
	if err := a.startCatalogQueue(func(context.Context, string) (downloadRequest, error) { return item, nil }); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, a)
	a.mu.Lock()
	state, msg, remaining := a.current.State, a.current.Error, len(a.catalogQueue)
	a.mu.Unlock()
	data, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(data, existing) || state != "failed" || remaining != 1 || !strings.Contains(msg, "does not match") {
		t.Fatalf("mismatched archive not preserved: %q %v %s %s %d", data, err, state, msg, remaining)
	}
}

func TestExistingArchiveVerificationRejectsLinksAndCancellation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("original")
	digest := sha256.Sum256(payload)
	item := downloadRequest{URL: "https://example.invalid/a.zim", Name: "a.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	target := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(target, payload, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, item.Name)
	if err := os.Symlink(target, link); err == nil {
		a := &app{root: root}
		if _, err := a.matchesInstalledArchive(context.Background(), item); err == nil {
			t.Fatal("accepted symlink")
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Logf("symlink unavailable: %v", err)
	}
	if err := os.WriteFile(link, payload, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &app{root: root}
	if _, err := a.matchesInstalledArchive(ctx, item); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel not honored: %v", err)
	}
}
