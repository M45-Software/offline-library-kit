package main

import "strings"

type queueSpaceReport struct {
	ReservedDownloadBytes    uint64 `json:"reservedDownloadBytes"`
	ReservedPreparationBytes uint64 `json:"reservedPreparationBytes"`
	RemainingDownloadBytes   uint64 `json:"remainingDownloadBytes"`
	RemainingUnknown         bool   `json:"remainingUnknown"`
}

func saturatingAdd(a, b uint64) uint64 {
	if b > ^uint64(0)-a {
		return ^uint64(0)
	}
	return a + b
}
func queueSpaceSnapshot(budget *queueSpaceBudget, pending []queuedCollection, running []map[string]any, partials []savedPartial, j job, last *downloadRequest) queueSpaceReport {
	var report queueSpaceReport
	remaining := map[string]uint64{}
	if budget != nil {
		budget.mu.Lock()
		for worker, reservation := range budget.reservations {
			report.ReservedDownloadBytes = saturatingAdd(report.ReservedDownloadBytes, reservation.remaining)
			report.ReservedPreparationBytes = saturatingAdd(report.ReservedPreparationBytes, reservation.extra)
			remaining[worker.queueID] = reservation.remaining
		}
		budget.mu.Unlock()
	}
	estimate := func(id string, size int64) {
		if size <= 0 {
			report.RemainingUnknown = true
			return
		}
		done := int64(0)
		for _, part := range partials {
			if part.CatalogID == id && id != "" && part.CanResume {
				done = max(done, part.Done)
			}
		}
		report.RemainingDownloadBytes = saturatingAdd(report.RemainingDownloadBytes, uint64(max(0, size-done)))
	}
	for _, item := range pending {
		estimate(item.ID, item.ApproxBytes)
	}
	for _, item := range running {
		id, _ := item["id"].(string)
		if bytes, known := remaining[id]; known {
			report.RemainingDownloadBytes = saturatingAdd(report.RemainingDownloadBytes, bytes)
		} else {
			progress, _ := item["job"].(job)
			if progress.State == "verifying" || progress.State == "repairing" || progress.State == "indexing" || progress.State == "complete" || progress.State == "waiting" && progress.Total > 0 && progress.Done >= progress.Total {
				continue
			}
			size, _ := item["approxBytes"].(int64)
			estimate(id, size)
		}
	}
	if len(running) == 0 && last != nil && activeDownload(j.State) && !strings.HasPrefix(j.Name, "Copying ") {
		bytes := int64(0)
		if j.State == "downloading" {
			bytes = max(0, last.Bytes-j.Done)
		} else if j.State == "checking" || j.State == "resolving" || j.State == "retrying" || j.State == "waiting" {
			bytes = last.Bytes
			for _, part := range partials {
				if part.Name == last.Name && part.CanResume {
					bytes = max(0, last.Bytes-part.Done)
				}
			}
		}
		report.ReservedDownloadBytes = uint64(bytes)
		report.RemainingDownloadBytes = saturatingAdd(report.RemainingDownloadBytes, uint64(bytes))
	}
	return report
}
