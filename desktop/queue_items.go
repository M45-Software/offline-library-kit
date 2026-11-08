package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

func (a *app) catalogResolver() queueResolver {
	return func(ctx context.Context, id string) (downloadRequest, error) {
		metadataCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		item, err := resolveCollection(metadataCtx, a.client, kiwixCatalog, id)
		item.CatalogID = id
		return item, err
	}
}
func (a *app) notifyQueueLocked() {
	if a.queueWake != nil {
		select {
		case a.queueWake <- struct{}{}:
		default:
		}
	}
}
func (a *app) eligibleQueueIndexLocked(scope map[string]bool) int {
	for i, item := range a.catalogQueue {
		if !item.Paused && !item.NetworkFailure && (scope == nil || scope[item.ID]) {
			return i
		}
	}
	return -1
}
func (a *app) addQueueItemHandlers(mux *http.ServeMux) {
	for _, pause := range []bool{true, false} {
		endpoint := "/api/queue/resume-item"
		if pause {
			endpoint = "/api/queue/pause-item"
		}
		mux.HandleFunc(endpoint, func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				ID string `json:"id"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request); err != nil || !uuidPattern.MatchString(request.ID) {
				http.Error(w, "Invalid collection", 400)
				return
			}
			if err := a.setQueueItemPaused(strings.ToLower(request.ID), pause); err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
			writeJSON(w, map[string]bool{"paused": pause})
		})
	}
}
func (a *app) setQueueItemPaused(id string, pause bool) error {
	a.mu.Lock()
	if a.closing || a.queueActive && a.queueContext != nil && a.queueContext.Err() != nil {
		a.mu.Unlock()
		return errors.New("Wait for the download list to finish pausing")
	}
	if a.queueItemActiveLocked(id) {
		var entry *queuedCollection
		for i := range a.queueInFlight {
			if a.queueInFlight[i].ID == id {
				entry = &a.queueInFlight[i]
				break
			}
		}
		if !pause && !entry.Paused {
			a.mu.Unlock()
			return nil
		}
		if !pause {
			a.mu.Unlock()
			return errors.New("Wait for this collection to pause before resuming it")
		}
		old := entry.Paused
		entry.Paused = true
		a.syncQueueCurrentLocked()
		if err := a.saveQueueLocked(a.queueCurrent, a.catalogQueue); err != nil {
			entry.Paused = old
			a.syncQueueCurrentLocked()
			a.mu.Unlock()
			return err
		}
		if cancel := a.queueItemCancels[id]; cancel != nil {
			cancel()
		}
		current := a.queueJobs[id]
		current.State = "pausing"
		current.Error = "Pausing this collection…"
		a.queueJobs[id] = current
		a.mu.Unlock()
		return nil
	}
	index := -1
	for i, item := range a.catalogQueue {
		if item.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		a.mu.Unlock()
		return errors.New("Collection is no longer in the list")
	}
	old := a.catalogQueue[index].Paused
	oldFailure := a.catalogQueue[index].NetworkFailure
	a.catalogQueue[index].Paused = pause
	if !pause {
		a.catalogQueue[index].NetworkFailure = false
	}
	if err := a.saveQueueLocked(a.queueCurrent, a.catalogQueue); err != nil {
		a.catalogQueue[index].Paused = old
		a.catalogQueue[index].NetworkFailure = oldFailure
		a.mu.Unlock()
		return err
	}
	active := a.queueActive
	resolve := a.queueResolve
	if resolve == nil {
		resolve = a.catalogResolver()
	}
	if !pause && active && a.queueScope != nil {
		a.queueScope[id] = true
	}
	a.notifyQueueLocked()
	a.mu.Unlock()
	if !pause && !active {
		if err := a.startCatalogQueueScoped(resolve, map[string]bool{id: true}); err != nil {
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.queueActive {
				if a.queueScope != nil {
					a.queueScope[id] = true
				}
				a.notifyQueueLocked()
				return nil
			}
			return err
		}
	}
	return nil
}
