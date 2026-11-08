package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func queueAPICall(a *app, endpoint string, value any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(value)
	r := httptest.NewRequest("POST", "http://127.0.0.1:9999"+endpoint, bytes.NewReader(b))
	r.Header.Set("X-Library-Token", "secret")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	return w
}

func TestQueueEstimatesPersistAndOldListsRemainReadable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, queueFilename)
	old := []byte(`{"version":1,"items":[{"id":"` + queueID1 + `","title":"Old"}]}`)
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{root: root, token: "secret"}
	if err := a.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if a.catalogQueue[0].ApproxBytes != 0 {
		t.Fatal("old size should be unknown")
	}
	if w := queueAPICall(a, "/api/queue/add", queuedCollection{ID: queueID2, Title: "New", ApproxBytes: 123456}); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	restored := &app{root: root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if len(restored.catalogQueue) != 2 || restored.catalogQueue[0].ApproxBytes != 0 || restored.catalogQueue[1].ApproxBytes != 123456 {
		t.Fatalf("wrong restored sizes: %+v", restored.catalogQueue)
	}
	for _, size := range []int64{-1, maxQueueEstimate + 1} {
		item := queuedCollection{ID: queueID1, Title: "Invalid", ApproxBytes: size}
		if w := queueAPICall(a, "/api/queue/add", item); w.Code != http.StatusBadRequest {
			t.Fatalf("accepted size %d: %d", size, w.Code)
		}
	}
}

func TestLibraryReservePersistsAndGuardsDownloads(t *testing.T) {
	root := t.TempDir()
	a := &app{root: root, token: "secret", client: http.DefaultClient}
	if err := a.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if a.effectiveReserveGiB() != 2 {
		t.Fatal("wrong default reserve")
	}
	for _, value := range []int{1, maxReserveGiB + 1} {
		if w := queueAPICall(a, "/api/reserve", map[string]int{"gib": value}); w.Code != http.StatusBadRequest {
			t.Fatalf("accepted %d GiB: %d", value, w.Code)
		}
	}
	if w := queueAPICall(a, "/api/reserve", map[string]int{"gib": maxReserveGiB}); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	restored := &app{root: root, token: "secret", client: http.DefaultClient}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if restored.effectiveReserveGiB() != maxReserveGiB {
		t.Fatal("reserve did not persist")
	}
	req := downloadRequest{URL: "https://example.com/reserve.zim", Name: "reserve.zim", Bytes: 1, SHA256: strings.Repeat("a", 64)}
	if err := restored.download(context.Background(), req); err == nil || !strings.Contains(err.Error(), "1000000 GiB reserve") {
		t.Fatalf("reserve not enforced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "content", "zim", "reserve.zim.part")); !os.IsNotExist(err) {
		t.Fatalf("created partial despite reserve: %v", err)
	}
	restored.current = job{State: "downloading"}
	if w := queueAPICall(restored, "/api/reserve", map[string]int{"gib": 2}); w.Code != http.StatusConflict {
		t.Fatal("changed reserve while downloading")
	}
	restored.current = job{State: "idle"}
	if w := queueAPICall(restored, "/api/reserve", map[string]int{"gib": 2}); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	reopened := &app{root: root}
	if err := reopened.initializeQueueStore(); err != nil || reopened.effectiveReserveGiB() != 2 {
		t.Fatalf("default not restored: %v", err)
	}
}

func TestChangedOrUnreadableDownloadSettingsBlockManualDownload(t *testing.T) {
	root := t.TempDir()
	a := &app{root: root, token: "secret", client: http.DefaultClient}
	if err := a.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if w := queueAPICall(a, "/api/reserve", map[string]int{"gib": 50}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	path := filepath.Join(root, queueFilename)
	if err := os.WriteFile(path, []byte(`{"version":1,"items":[],"reserveGiB":100}`), 0600); err != nil {
		t.Fatal(err)
	}
	req := downloadRequest{URL: "https://example.com/file.zim", Name: "file.zim", Bytes: 1, SHA256: strings.Repeat("a", 64)}
	if err := a.download(context.Background(), req); err == nil || !strings.Contains(err.Error(), "changed outside") {
		t.Fatalf("changed settings accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "content", "zim", "file.zim.part")); !os.IsNotExist(err) {
		t.Fatalf("partial created with changed settings: %v", err)
	}
	a.queueStoreError = "settings unreadable"
	if err := a.download(context.Background(), req); err == nil || !strings.Contains(err.Error(), "settings unreadable") {
		t.Fatalf("unreadable settings accepted: %v", err)
	}
}

func TestInvalidSavedReserveIsNotTreatedAsDefault(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, queueFilename)
	invalid := []byte(`{"version":1,"items":[],"reserveGiB":1}`)
	if err := os.WriteFile(path, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{root: root}
	if err := a.initializeQueueStore(); err == nil {
		t.Fatal("accepted an invalid saved reserve")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, invalid) {
		t.Fatalf("invalid settings changed: %q %v", data, err)
	}
}

func TestQueueStoreRestoresPausedAndLocks(t *testing.T) {
	root := t.TempDir()
	a := &app{downloadConcurrency: 1, root: root, token: "secret"}
	if err := a.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []queuedCollection{{ID: queueID1, Title: "First"}, {ID: queueID2, Title: "Second"}} {
		if w := queueAPICall(a, "/api/queue/add", item); w.Code != http.StatusOK {
			t.Fatalf("add: %d %s", w.Code, w.Body)
		}
	}
	unlock, err := a.lockQueueStore()
	if err != nil {
		t.Fatal(err)
	}
	other := &app{root: root}
	if err := other.initializeQueueStore(); err == nil {
		unlock()
		t.Fatal("second app obtained queue lock")
	}
	unlock()
	started := make(chan struct{})
	if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
		close(started)
		<-ctx.Done()
		return downloadRequest{}, ctx.Err()
	}); err != nil {
		t.Fatal("queue did not start:", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("resolver did not start")
	}
	record, exists, _, err := readQueueRecord(filepath.Join(root, queueFilename))
	if err != nil || !exists || len(record.Items) != 2 || record.Items[0].ID != queueID1 {
		t.Fatalf("active item not saved: %+v %v", record, err)
	}
	a.mu.Lock()
	cancel := a.cancelDownload
	a.mu.Unlock()
	cancel()
	waitQueue(t, a)
	restored := &app{root: root, token: "secret"}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if restored.queueActive || len(restored.catalogQueue) != 2 || restored.catalogQueue[0].ID != queueID1 {
		t.Fatalf("incorrect restored queue: %+v", restored.catalogQueue)
	}
	if w := queueAPICall(restored, "/api/queue/remove", map[string]string{"id": queueID1}); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	record, _, _, err = readQueueRecord(filepath.Join(root, queueFilename))
	if err != nil || len(record.Items) != 1 || record.Items[0].ID != queueID2 {
		t.Fatalf("removal not saved: %+v %v", record, err)
	}
}

func TestQueueStorePreservesUnrecognizedAndChangedFiles(t *testing.T) {
	t.Run("unrecognized", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, queueFilename)
		original := []byte("do not overwrite")
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		a := &app{root: root}
		if err := a.initializeQueueStore(); err == nil {
			t.Fatal("unrecognized file accepted")
		}
		data, _ := os.ReadFile(path)
		if !bytes.Equal(data, original) {
			t.Fatal("unrecognized file changed")
		}
	})
	t.Run("changed externally", func(t *testing.T) {
		root := t.TempDir()
		a := &app{root: root, token: "secret"}
		if err := a.initializeQueueStore(); err != nil {
			t.Fatal(err)
		}

		if w := queueAPICall(a, "/api/queue/add", queuedCollection{ID: queueID1, Title: "First"}); w.Code != 200 {
			t.Fatal(w.Body)
		}
		external := []byte(`{"version":1,"items":[{"id":"` + queueID2 + `","title":"External"}]}`)
		path := filepath.Join(root, queueFilename)
		if err := os.WriteFile(path, external, 0600); err != nil {
			t.Fatal(err)
		}
		if w := queueAPICall(a, "/api/queue/add", queuedCollection{ID: queueID2, Title: "Second"}); w.Code != http.StatusConflict {
			t.Fatalf("changed file accepted: %d", w.Code)
		}
		if err := a.startCatalogQueue(func(context.Context, string) (downloadRequest, error) {
			t.Error("stale queue started")
			return downloadRequest{}, nil
		}); err == nil {
			t.Fatal("started a stale list")
		}
		data, _ := os.ReadFile(path)
		if !bytes.Equal(data, external) {
			t.Fatal("external file overwritten")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "keep")
		if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, queueFilename)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		a := &app{root: root}
		if err := a.initializeQueueStore(); err == nil {
			t.Fatal("symlink accepted")
		}
		data, _ := os.ReadFile(target)
		if string(data) != "keep" {
			t.Fatal("symlink target changed")
		}
	})
}

func TestQueueStoreRemovesOnlyCompletedItem(t *testing.T) {
	payload := []byte("saved queue fixture")
	digest := sha256.Sum256(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	root := t.TempDir()
	a := &app{downloadConcurrency: 1, root: root, client: server.Client(), token: "secret"}
	if err := a.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []queuedCollection{{ID: queueID1, Title: "First"}, {ID: queueID2, Title: "Second"}} {
		if w := queueAPICall(a, "/api/queue/add", item); w.Code != http.StatusOK {
			t.Fatal(w.Body)
		}
	}
	resolve := func(ctx context.Context, id string) (downloadRequest, error) {
		if id == queueID2 {
			return downloadRequest{}, errors.New("second lookup failed")
		}
		return downloadRequest{URL: server.URL, Name: "first.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), CatalogID: id}, nil
	}
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal("queue did not start")
	}
	waitQueue(t, a)
	if data, err := os.ReadFile(filepath.Join(root, "content", "zim", "first.zim")); err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("first item missing: %q %v", data, err)
	}
	restored := &app{root: root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if restored.queueActive || len(restored.catalogQueue) != 1 || restored.catalogQueue[0].ID != queueID2 {
		t.Fatalf("wrong restored items: %+v", restored.catalogQueue)
	}
}

func TestQueueStorePausesIfListChangesDuringCompletedDownload(t *testing.T) {
	payload := []byte("completed but list changed")
	digest := sha256.Sum256(payload)
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	root := t.TempDir()
	a := &app{root: root, client: server.Client(), token: "secret"}
	if err := a.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if w := queueAPICall(a, "/api/queue/add", queuedCollection{ID: queueID1, Title: "First"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	resolve := func(ctx context.Context, id string) (downloadRequest, error) {
		return downloadRequest{URL: server.URL, Name: "completed.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), CatalogID: id}, nil
	}
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal("queue did not start")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("resolver did not start")
	}
	external := []byte(`{"version":1,"items":[{"id":"` + queueID2 + `","title":"External"}]}`)
	path := filepath.Join(root, queueFilename)
	if err := os.WriteFile(path, external, 0600); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitQueue(t, a)
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, external) {
		t.Fatalf("external list overwritten: %q %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(root, "content", "zim", "completed.zim"))
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("completed archive missing: %q %v", data, err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current.State != "failed" || len(a.catalogQueue) != 1 || a.catalogQueue[0].ID != queueID1 {
		t.Fatalf("list not safely paused: %+v %+v", a.current, a.catalogQueue)
	}
}

type crashFixtureBody struct {
	ctx  context.Context
	sent bool
}

func (b *crashFixtureBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, []byte("abc")), nil
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *crashFixtureBody) Close() error { return nil }

// This helper is deliberately killed by the parent test after it writes a
// partial. It never contacts the network or a user's library.
func TestQueueCrashHelper(t *testing.T) {
	root := os.Getenv("OLK_QUEUE_CRASH_ROOT")
	if root == "" {
		return
	}
	identity, err := directoryIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &crashFixtureBody{ctx: r.Context()}}, nil
	})}
	a := &app{downloadConcurrency: 1, root: root, rootIdentity: identity, token: "secret", client: client}
	if err := a.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []queuedCollection{{ID: queueID1, Title: "First"}, {ID: queueID2, Title: "Second"}} {
		if w := queueAPICall(a, "/api/queue/add", item); w.Code != http.StatusOK {
			t.Fatal(w.Body)
		}
	}
	payload := []byte("abcdef")
	digest := sha256.Sum256(payload)
	if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
		return downloadRequest{URL: "https://example.invalid/crash.zim", Name: "crash.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), CatalogID: id}, nil
	}); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestQueueRestoresAfterProcessTermination(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestQueueCrashHelper$")
	cmd.Env = append(os.Environ(), "OLK_QUEUE_CRASH_ROOT="+root)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	part := filepath.Join(root, "content", "zim", "crash.zim.part")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(part); err == nil && string(data) == "abc" {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("helper did not write partial: %s", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	identity, err := directoryIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	restored := &app{root: root, rootIdentity: identity}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	if restored.queueActive || len(restored.catalogQueue) != 2 || restored.catalogQueue[0].ID != queueID1 || restored.catalogQueue[1].ID != queueID2 {
		t.Fatalf("incorrect restored list: %+v", restored.catalogQueue)
	}
	digest := sha256.Sum256([]byte("abcdef"))
	item := downloadRequest{URL: "https://example.invalid/crash.zim", Name: "crash.zim", Bytes: 6, SHA256: hex.EncodeToString(digest[:]), CatalogID: queueID1}
	if !matchingPartial(root, item) {
		t.Fatal("partial or resume metadata was not retained")
	}
	if _, err := os.Stat(filepath.Join(root, "content", "zim", "crash.zim")); !os.IsNotExist(err) {
		t.Fatal("partial installed as final")
	}
	// Continue the same saved item after the process was killed, checking that
	// the original bytes are neither downloaded twice nor treated as complete.
	restored.client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Range") != "bytes=3-" {
			t.Errorf("crash recovery lost Range: %s", r.Header.Get("Range"))
		}
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": []string{"bytes 3-5/6"}}, Body: io.NopCloser(strings.NewReader("def"))}, nil
	})}
	restored.queueResolve = func(context.Context, string) (downloadRequest, error) { return item, nil }
	if err := restored.setQueueItemPaused(queueID1, false); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, restored)
	data, err := os.ReadFile(filepath.Join(root, "content", "zim", "crash.zim"))
	if err != nil || string(data) != "abcdef" || len(restored.catalogQueue) != 1 || restored.catalogQueue[0].ID != queueID2 {
		t.Fatalf("crash resume failed: %q %v %+v", data, err, restored.catalogQueue)
	}
	if len(restored.downloadHistory) != 1 || restored.downloadHistory[0].State != "complete" {
		t.Fatal("recovered completion not recorded")
	}
}
