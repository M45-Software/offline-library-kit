package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The saved queue retains the active item until verified installation completes.
// A restored queue remains paused until the user starts it.
type queuedCollection struct {
	NetworkFailure bool   `json:"networkFailure,omitempty"`
	Paused         bool   `json:"paused,omitempty"`
	ID             string `json:"id"`
	Title          string `json:"title"`
	ApproxBytes    int64  `json:"approxBytes,omitempty"`
}

// Keep aggregate estimates exactly representable in JavaScript, even for 50 items.
const maxQueueEstimate = int64(100_000_000_000_000)

type queueResolver func(context.Context, string) (downloadRequest, error)

func (a *app) addQueueHandlers(mux *http.ServeMux) {
	a.addQueueItemHandlers(mux)
	mux.HandleFunc("/api/queue/concurrency", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Concurrency  int  `json:"concurrency"`
			StallSeconds *int `json:"stallSeconds"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request); err != nil || request.Concurrency < 1 || request.Concurrency > maxDownloadConcurrency {
			http.Error(w, "Choose 1 to 8 simultaneous downloads", 400)
			return
		}
		if request.StallSeconds != nil && (*request.StallSeconds < minStallSeconds || *request.StallSeconds > maxStallSeconds) {
			http.Error(w, "Choose a stall timeout from 15 to 600 seconds", 400)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closing || a.queueActive || activeDownload(a.current.State) {
			http.Error(w, "Pause downloads before changing download settings", 409)
			return
		}
		previous := a.downloadConcurrency
		previousStall := a.stallSeconds
		if request.StallSeconds != nil {
			a.stallSeconds = *request.StallSeconds
			if a.stallSeconds == defaultStallSeconds {
				a.stallSeconds = 0
			}
		}
		a.downloadConcurrency = request.Concurrency
		if request.Concurrency == defaultDownloadConcurrency {
			a.downloadConcurrency = 0
		}
		if err := a.saveQueueLocked(a.queueCurrent, a.catalogQueue); err != nil {
			a.downloadConcurrency = previous
			a.stallSeconds = previousStall
			http.Error(w, "Could not save download settings: "+err.Error(), 409)
			return
		}
		writeJSON(w, map[string]int{"concurrency": request.Concurrency, "stallSeconds": a.effectiveStallSeconds()})
	})
	mux.HandleFunc("/api/reserve", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			GiB int `json:"gib"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request); err != nil || request.GiB < 2 || request.GiB > maxReserveGiB {
			http.Error(w, "Reserve must be a whole number between 2 and 1000000 GiB", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closing || a.queueActive || activeDownload(a.current.State) {
			http.Error(w, "Stop the current download before changing the reserve", http.StatusConflict)
			return
		}
		previous := a.reserveGiB
		a.reserveGiB = request.GiB
		if a.reserveGiB == 2 {
			a.reserveGiB = 0
		}
		if err := a.saveQueueLocked(a.queueCurrent, a.catalogQueue); err != nil {
			a.reserveGiB = previous
			http.Error(w, "Could not save reserve: "+err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"saved": true})
	})
	mux.HandleFunc("/api/queue/add", func(w http.ResponseWriter, r *http.Request) {
		var item queuedCollection
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&item); err != nil || !uuidPattern.MatchString(item.ID) {
			http.Error(w, "Invalid collection", http.StatusBadRequest)
			return
		}
		item.ID = strings.ToLower(item.ID)
		// Failure state belongs to completed worker attempts, never new additions.
		item.NetworkFailure = false
		item.Title = strings.TrimSpace(item.Title)
		if item.Title == "" || len(item.Title) > 200 {
			http.Error(w, "Invalid collection title", http.StatusBadRequest)
			return
		}
		if item.ApproxBytes < 0 || item.ApproxBytes > maxQueueEstimate {
			http.Error(w, "Invalid collection size estimate", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closing || len(a.catalogQueue)+len(a.queueInFlight) >= 50 {
			http.Error(w, "Download list is unavailable or full", http.StatusConflict)
			return
		}
		if a.queueItemActiveLocked(item.ID) {
			http.Error(w, "Collection is already downloading", http.StatusConflict)
			return
		}
		for _, queued := range a.catalogQueue {
			if queued.ID == item.ID {
				http.Error(w, "Collection is already in the list", http.StatusConflict)
				return
			}
		}
		next := append(append([]queuedCollection{}, a.catalogQueue...), item)
		if err := a.saveQueueLocked(a.queueCurrent, next); err != nil {
			http.Error(w, "Could not save download list: "+err.Error(), http.StatusConflict)
			return
		}
		a.catalogQueue = next
		a.notifyQueueLocked()
		writeJSON(w, map[string]bool{"added": true})
	})
	mux.HandleFunc("/api/queue/remove", func(w http.ResponseWriter, r *http.Request) {
		var item struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&item); err != nil || !uuidPattern.MatchString(item.ID) {
			http.Error(w, "Invalid collection", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, queued := range a.catalogQueue {
			if strings.EqualFold(queued.ID, item.ID) {
				next := append(append([]queuedCollection{}, a.catalogQueue[:i]...), a.catalogQueue[i+1:]...)
				if err := a.saveQueueLocked(a.queueCurrent, next); err != nil {
					http.Error(w, "Could not save download list: "+err.Error(), http.StatusConflict)
					return
				}
				a.catalogQueue = next
				writeJSON(w, map[string]bool{"removed": true})
				return
			}
		}
		http.Error(w, "Collection is no longer in the list", http.StatusConflict)
	})
	mux.HandleFunc("/api/queue/start", func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkLibraryRoot(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		resolver := a.catalogResolver()
		if err := a.startCatalogQueue(resolver); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"started": true})
	})
}

// matchesInstalledArchive never modifies an existing file. It verifies both
// bytes and file identity before considering a queued item already complete.
func (a *app) matchesInstalledArchive(ctx context.Context, item downloadRequest) (bool, error) {
	if err := validRequest(item); err != nil {
		return false, err
	}
	if err := a.checkLibraryRoot(); err != nil {
		return false, err
	}
	dir := filepath.Join(a.root, "content", "zim")
	path := filepath.Join(dir, item.Name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("Cannot inspect existing archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("Existing destination is not a regular file; retained")
	}
	if info.Size() != item.Bytes {
		return false, errors.New("Existing archive size differs from publisher details; retained")
	}
	dirInfo, err := directoryIdentity(dir)
	if err != nil {
		return false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, errors.New("Existing archive changed while opening; retained")
	}
	prepareDownloadCache(f)
	h := sha256.New()
	buffer := make([]byte, 1024*1024)
	var checked, dropped int64
	a.setJob(job{State: "verifying", Name: item.Name, Total: item.Bytes})
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, readErr := f.Read(buffer)
		if n > 0 {
			_, _ = h.Write(buffer[:n])
			checked += int64(n)
			if checked > item.Bytes {
				return false, errors.New("Existing archive grew during verification; retained")
			}
			if checked-dropped >= 64*1024*1024 {
				discardDownloadCache(f, dropped, checked-dropped)
				dropped = checked
			}
			a.setJob(job{State: "verifying", Name: item.Name, Done: checked, Total: item.Bytes})
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return false, fmt.Errorf("Cannot read existing archive: %w", readErr)
		}
	}
	discardDownloadCache(f, 0, 0)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := a.checkLibraryRoot(); err != nil {
		return false, err
	}
	dirNow, err := directoryIdentity(dir)
	if err != nil || !os.SameFile(dirInfo, dirNow) {
		return false, errors.New("Library destination changed during verification; retained")
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) || current.Size() != opened.Size() || !current.ModTime().Equal(opened.ModTime()) {
		return false, errors.New("Existing archive changed during verification; retained")
	}
	if checked != item.Bytes || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), item.SHA256) {
		return false, errors.New("Existing archive does not match publisher SHA-256; retained")
	}
	return true, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
