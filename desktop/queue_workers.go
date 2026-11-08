package main

import (
	"context"
	"errors"
	"fmt"
)

const defaultDownloadConcurrency = 4
const maxDownloadConcurrency = 8

func (a *app) effectiveDownloadConcurrency() int {
	if a.downloadConcurrency == 0 {
		return defaultDownloadConcurrency
	}
	return a.downloadConcurrency
}
func (a *app) queueItemActiveLocked(id string) bool {
	for _, item := range a.queueInFlight {
		if item.ID == id {
			return true
		}
	}
	return false
}
func (a *app) queueItemPausedLocked(id string) bool {
	for _, item := range a.queueInFlight {
		if item.ID == id {
			return item.Paused
		}
	}
	return false
}
func (a *app) syncQueueCurrentLocked() {
	a.queueCurrent = nil
	if len(a.queueInFlight) > 0 {
		item := a.queueInFlight[0]
		a.queueCurrent = &item
	}
}
func (a *app) removeInFlightLocked(id string) {
	for i, item := range a.queueInFlight {
		if item.ID == id {
			a.queueInFlight = append(a.queueInFlight[:i], a.queueInFlight[i+1:]...)
			break
		}
	}
	delete(a.queueJobs, id)
	delete(a.queueItemCancels, id)
	a.syncQueueCurrentLocked()
}

type queueResult struct {
	queued     queuedCollection
	item       downloadRequest
	err        error
	retries    int
	httpStatus int
}

func (a *app) startCatalogQueue(resolve queueResolver) error {
	return a.startCatalogQueueScoped(resolve, nil)
}
func (a *app) startCatalogQueueScoped(resolve queueResolver, scope map[string]bool) error {
	a.mu.Lock()
	if a.closing || a.queueActive || activeDownload(a.current.State) || a.eligibleQueueIndexLocked(scope) < 0 {
		a.mu.Unlock()
		return errors.New("Download list has no unpaused items or another download is running")
	}
	if err := a.validateQueueLocked(); err != nil {
		a.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	limit := a.effectiveDownloadConcurrency()
	budget := newQueueSpaceBudget(a.root, a.effectiveReserveGiB())
	processing := make(chan struct{}, 1)
	a.activeBudget = budget
	limiter := a.limiterLocked()
	a.queueActive = true
	a.cancelDownload = cancel
	a.queueJobs = map[string]job{}
	a.queueItemCancels = map[string]context.CancelFunc{}
	a.queueWake = make(chan struct{}, 1)
	a.queueScope = scope
	a.queueContext = ctx
	a.queueResolve = resolve
	a.lastDownload = nil
	a.retryResolve = nil
	a.mu.Unlock()
	go func() {
		defer cancel()
		results := make(chan queueResult, maxDownloadConcurrency)
		running := 0
		var failure error
		var failedName string
		for {
			a.mu.Lock()
			for ctx.Err() == nil && running < limit {
				index := a.eligibleQueueIndexLocked(a.queueScope)
				if index < 0 {
					break
				}
				queued := a.catalogQueue[index]
				a.catalogQueue = append(append([]queuedCollection{}, a.catalogQueue[:index]...), a.catalogQueue[index+1:]...)
				a.queueInFlight = append(a.queueInFlight, queued)
				a.syncQueueCurrentLocked()
				itemCtx, cancelItem := context.WithCancel(ctx)
				a.queueItemCancels[queued.ID] = cancelItem
				initial := job{State: "resolving", Name: queued.Title}
				a.queueJobs[queued.ID] = initial
				a.current = initial
				// Workers own progress/settings; only the parent edits the persisted list.
				worker := &app{root: a.root, rootIdentity: a.rootIdentity, client: a.client, searchDir: a.searchDir, reserveGiB: a.reserveGiB, downloadBudget: budget, stallSeconds: a.stallSeconds, stallTimeout: a.stallTimeout, bandwidth: limiter, queueID: queued.ID}
				worker.jobSink = func(j job) {
					a.mu.Lock()
					defer a.mu.Unlock()
					if a.queueItemActiveLocked(queued.ID) {
						if a.queueItemPausedLocked(queued.ID) {
							j.State = "pausing"
							j.Error = "Pausing this collection…"
						}
						a.queueJobs[queued.ID] = j
						a.current = j
					}
				}
				worker.settingsValidator = func() error { a.mu.Lock(); defer a.mu.Unlock(); return a.validateQueueLocked() }
				running++
				go func() {
					result := queueResult{queued: queued}
					defer func() {
						if result.err != nil && itemCtx.Err() != nil {
							result.err = itemCtx.Err()
						}
						worker.mu.Lock()
						result.retries = worker.retryCount
						result.httpStatus = worker.lastHTTPStatus
						worker.mu.Unlock()
						cancelItem()
						budget.release(worker)
						results <- result
					}()
					result.err = worker.withNetworkRetries(itemCtx, queued.Title, func() error { var err error; result.item, err = resolve(itemCtx, queued.ID); return err })
					if result.err != nil {
						return
					}
					var installed bool
					installed, result.err = worker.matchesInstalledArchive(itemCtx, result.item)
					if result.err != nil {
						return
					}
					if installed {
						result.err = worker.reserveInstalledPreparation(itemCtx, result.item)
					} else {
						result.err = worker.downloadWithRetries(itemCtx, result.item)
					}
					if result.err != nil {
						return
					}
					worker.setJob(job{State: "waiting", Name: result.item.Name, Done: result.item.Bytes, Total: result.item.Bytes, Error: "Downloaded. Waiting for preparation…"})
					select {
					case <-itemCtx.Done():
						result.err = itemCtx.Err()
						return
					case processing <- struct{}{}:
					}
					defer func() { <-processing }()
					if err := itemCtx.Err(); err != nil {
						result.err = err
						return
					}
					result.err = worker.repairIFixit(itemCtx, result.item)
					if result.err == nil {
						result.err = worker.prepareCollectionSearch(itemCtx, result.item)
					}
					if result.err == nil {
						worker.setJob(job{State: "complete", Name: result.item.Name, Done: result.item.Bytes, Total: result.item.Bytes})
					}
				}()
			}
			if running == 0 {
				// Finalize under the scheduler lock so a simultaneous Resume starts a new run.
				a.catalogQueue = append(append([]queuedCollection{}, a.queueInFlight...), a.catalogQueue...)
				a.queueInFlight = nil
				a.queueJobs = nil
				a.queueItemCancels = nil
				a.queueCurrent = nil
				a.activeBudget = nil
				a.queueActive = false
				a.cancelDownload = nil
				a.queueWake = nil
				a.queueScope = nil
				a.queueContext = nil
				if failure != nil {
					a.current = job{State: "failed", Name: failedName, Error: failure.Error() + " Download list paused."}
				} else if ctx.Err() != nil {
					a.current = job{State: "canceled", Error: "Download list stopped. Matching partials and unfinished preparation were retained."}
				} else if len(a.catalogQueue) > 0 && a.eligibleQueueIndexLocked(nil) < 0 {
					a.current = job{State: "paused", Error: "All remaining collections are paused."}
					failed := 0
					for _, item := range a.catalogQueue {
						if item.NetworkFailure {
							failed++
						}
					}
					if failed > 0 {
						label := "collections need"
						if failed == 1 {
							label = "collection needs"
						}
						a.current.Error = fmt.Sprintf("%d %s a network retry. Saved progress retained.", failed, label)
					}
				}
				a.mu.Unlock()
				return
			}
			wake := a.queueWake
			a.mu.Unlock()
			var result queueResult
			select {
			case result = <-results:
				running--
			case <-wake:
				continue
			}
			a.mu.Lock()
			itemPaused := ctx.Err() == nil && a.queueItemPausedLocked(result.queued.ID) && errors.Is(result.err, context.Canceled)
			var exhausted *exhaustedNetworkRetriesError
			isolated := ctx.Err() == nil && errors.As(result.err, &exhausted)
			oldHistory := a.downloadHistory
			terminal := result.err == nil || !errors.Is(result.err, context.Canceled)
			if terminal {
				a.appendHistoryLocked(result.queued.ID, result.queued.Title, result.err, result.retries, result.httpStatus)
			}
			if result.err == nil || itemPaused || isolated {
				old := append([]queuedCollection{}, a.queueInFlight...)
				oldPending := append([]queuedCollection{}, a.catalogQueue...)
				a.removeInFlightLocked(result.queued.ID)
				if itemPaused || isolated {
					paused := result.queued
					paused.Paused = true
					paused.NetworkFailure = isolated
					a.catalogQueue = append(a.catalogQueue, paused)
				}
				if err := a.saveQueueLocked(nil, a.catalogQueue); err != nil {
					a.downloadHistory = oldHistory
					a.queueInFlight = old
					a.catalogQueue = oldPending
					a.syncQueueCurrentLocked()
					result.err = fmt.Errorf("Collection stopped, but the list could not be saved: %w", err)
					itemPaused = false
					isolated = false
				} else if !itemPaused && !isolated {
					a.current = job{State: "complete", Name: result.item.Name, Done: result.item.Bytes, Total: result.item.Bytes}
				}
			} else if terminal {
				if err := a.saveQueueLocked(a.queueCurrent, a.catalogQueue); err != nil {
					a.downloadHistory = oldHistory
					result.err = fmt.Errorf("%v; history could not be saved: %w", result.err, err)
				}
			}
			if result.err != nil && !itemPaused && !isolated && failure == nil && !errors.Is(result.err, context.Canceled) {
				failure = result.err
				failedName = result.queued.Title
				cancel()
			}
			a.mu.Unlock()
		}
	}()
	return nil
}
