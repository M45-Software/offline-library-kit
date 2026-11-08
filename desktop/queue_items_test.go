package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func waitForPausedItem(t *testing.T, a *app, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		paused := false
		for _, item := range a.catalogQueue {
			if item.ID == id && item.Paused {
				paused = true
			}
		}
		a.mu.Unlock()
		if paused {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("item did not pause")
}
func TestPauseOneItemLeavesOtherWorkersRunningAndFillsSlot(t *testing.T) {
	a := &app{root: t.TempDir(), token: "secret", downloadConcurrency: 2}
	items := parallelItems(3)
	seedParallelQueue(t, a, items)
	started := make(chan string, 4)
	if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
		started <- id
		<-ctx.Done()
		return downloadRequest{}, ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		a.mu.Lock()
		cancel := a.cancelDownload
		a.mu.Unlock()
		if cancel != nil {
			cancel()
			waitQueue(t, a)
		}
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	if response := queueAPICall(a, "/api/queue/pause-item", map[string]string{"id": items[0].ID}); response.Code != 200 {
		t.Fatal(response.Body)
	}
	waitForPausedItem(t, a, items[0].ID)
	select {
	case id := <-started:
		if id != items[2].ID {
			t.Fatalf("wrong replacement %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("freed slot not reused")
	}
	a.mu.Lock()
	active, otherRunning := a.queueActive, a.queueItemActiveLocked(items[1].ID)
	a.mu.Unlock()
	if !active || !otherRunning {
		t.Fatal("pausing one stopped another")
	}
	record, _, _, err := readQueueRecord(a.root + "/" + queueFilename)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range record.Items {
		if item.ID == items[0].ID {
			found = item.Paused
		}
	}
	if !found {
		t.Fatal("individual pause not persisted")
	}
}
func TestResumeFromStoppedListStartsOnlyChosenItem(t *testing.T) {
	a := &app{root: t.TempDir(), token: "secret"}
	items := parallelItems(3)
	seedParallelQueue(t, a, items)
	started := make(chan string, 4)
	a.queueResolve = func(ctx context.Context, id string) (downloadRequest, error) {
		started <- id
		<-ctx.Done()
		return downloadRequest{}, ctx.Err()
	}
	if response := queueAPICall(a, "/api/queue/resume-item", map[string]string{"id": items[1].ID}); response.Code != 200 {
		t.Fatal(response.Body)
	}
	select {
	case id := <-started:
		if id != items[1].ID {
			t.Fatal("resumed wrong item")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resume did not start")
	}
	select {
	case id := <-started:
		t.Fatalf("unexpected item started: %s", id)
	case <-time.After(30 * time.Millisecond):
	}
	a.mu.Lock()
	cancel := a.cancelDownload
	a.mu.Unlock()
	cancel()
	waitQueue(t, a)
}
func TestPausedItemSurvivesRestartAndAllPausedListStaysStopped(t *testing.T) {
	a := &app{root: t.TempDir(), token: "secret"}
	items := parallelItems(2)
	seedParallelQueue(t, a, items)
	for _, item := range items {
		if response := queueAPICall(a, "/api/queue/pause-item", map[string]string{"id": item.ID}); response.Code != 200 {
			t.Fatal(response.Body)
		}
	}
	restored := &app{root: a.root}
	if err := restored.initializeQueueStore(); err != nil {
		t.Fatal(err)
	}
	for _, item := range restored.catalogQueue {
		if !item.Paused {
			t.Fatal("pause lost on restart")
		}
	}
	if err := restored.startCatalogQueue(func(context.Context, string) (downloadRequest, error) {
		return downloadRequest{}, fmt.Errorf("must not start")
	}); err == nil {
		t.Fatal("all-paused list started")
	}
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9999/api/queue/pause-item", nil)
	req.Header.Set("X-Library-Token", "secret")
	a.handler().ServeHTTP(response, req)
	if response.Code != 400 {
		t.Fatal("accepted invalid item request")
	}
}

func TestResumeItemWakesSchedulerWhileAnotherDownloadContinues(t *testing.T) {
	a := &app{root: t.TempDir(), token: "secret", downloadConcurrency: 2}
	items := parallelItems(3)
	items[1].Paused = true
	items[2].Paused = true
	seedParallelQueue(t, a, items)
	started := make(chan string, 4)
	if err := a.startCatalogQueue(func(ctx context.Context, id string) (downloadRequest, error) {
		started <- id
		<-ctx.Done()
		return downloadRequest{}, ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		a.mu.Lock()
		cancel := a.cancelDownload
		a.mu.Unlock()
		if cancel != nil {
			cancel()
			waitQueue(t, a)
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first worker did not start")
	}
	if response := queueAPICall(a, "/api/queue/resume-item", map[string]string{"id": items[1].ID}); response.Code != 200 {
		t.Fatal(response.Body)
	}
	select {
	case id := <-started:
		if id != items[1].ID {
			t.Fatal("wrong resumed item")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resume waited for an unrelated download")
	}
	select {
	case id := <-started:
		t.Fatalf("started a paused item: %s", id)
	default:
	}
}
