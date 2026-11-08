package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const benchmarkBytes = 32 * 1024 * 1024

func (a *app) addBenchmarkHandler(mux *http.ServeMux) {
	mux.HandleFunc("/api/benchmark", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path       string `json:"path"`
			Mode       string `json:"mode"`
			ReserveGiB int    `json:"reserveGiB"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil || !filepath.IsAbs(req.Path) || (req.Mode != "quick" && req.Mode != "extended") || req.ReserveGiB < 2 || req.ReserveGiB > maxReserveGiB {
			http.Error(w, "Choose a folder before testing speed", 400)
			return
		}
		a.mu.Lock()
		if a.closing || a.queueActive || activeDownload(a.current.State) {
			a.mu.Unlock()
			http.Error(w, "Wait for the current operation to finish", 409)
			return
		}
		previous := a.current
		ctx, cancel := context.WithCancel(r.Context())
		a.cancelDownload = cancel
		a.current = job{State: "checking", Name: "Testing drive speed"}
		a.mu.Unlock()
		size, operations := benchmarkBytes, 8
		if req.Mode == "extended" {
			size, operations = 16*1024*1024*1024, 8192
		}
		reserve := uint64(req.ReserveGiB) * gibibyte
		measure, err := benchmarkWrite(ctx, req.Path, size, reserve, func(done int) {
			a.setJob(job{State: "checking", Name: "Testing sequential write", Done: int64(done), Total: int64(size)})
		})
		var mixed float64
		if err == nil {
			a.setJob(job{State: "checking", Name: "Testing concurrent mixed access"})
			mixed, err = benchmarkMixedWithOps(ctx, req.Path, operations, reserve)
		}
		a.mu.Lock()
		a.cancelDownload = nil
		a.current = previous
		a.mu.Unlock()
		cancel()
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeJSON(w, map[string]any{"bytesPerSecond": measure.overall, "firstHalfBytesPerSecond": measure.first, "secondHalfBytesPerSecond": measure.second, "mixedBytesPerSecond": mixed, "bytes": size, "mixedFiles": 3, "mixedOperationsPerFile": operations, "mode": req.Mode})
	})
}
func benchmarkFolder(ctx context.Context, folder string) (float64, error) {
	result, err := benchmarkWrite(ctx, folder, benchmarkBytes, freeSpaceReserve, nil)
	return result.overall, err
}

type writeMeasurement struct{ overall, first, second float64 }

func benchmarkWrite(ctx context.Context, folder string, size int, reserve uint64, progress func(int)) (writeMeasurement, error) {
	info, err := directoryIdentity(folder)
	if err != nil {
		return writeMeasurement{}, errors.New("The selected folder is unavailable")
	}
	free, err := availableSpace(folder)
	if err != nil {
		return writeMeasurement{}, fmt.Errorf("Cannot check available space: %w", err)
	}
	if !spaceSufficient(free, uint64(size), reserve) {
		return writeMeasurement{}, errors.New("Not enough free space for the speed test and selected reserve")
	}
	buf := make([]byte, 1024*1024)
	if _, err := rand.Read(buf); err != nil {
		return writeMeasurement{}, err
	}
	f, err := os.CreateTemp(folder, ".offline-library-speed-*.tmp")
	if err != nil {
		return writeMeasurement{}, fmt.Errorf("Cannot create speed test file: %w", err)
	}
	path := f.Name()
	defer func() {
		f.Close()
		current, e := directoryIdentity(folder)
		if e == nil && os.SameFile(info, current) {
			os.Remove(path)
		}
	}()
	start := time.Now()
	var halfway time.Time
	for n := 0; n < size/len(buf); n++ {
		if err := ctx.Err(); err != nil {
			return writeMeasurement{}, err
		}
		current, e := directoryIdentity(folder)
		if e != nil || !os.SameFile(info, current) {
			return writeMeasurement{}, errors.New("Selected drive changed during the test")
		}
		if _, err := f.Write(buf); err != nil {
			return writeMeasurement{}, err
		}
		if (n+1)%64 == 0 || n+1 == size/len(buf)/2 {
			if err := f.Sync(); err != nil {
				return writeMeasurement{}, err
			}
			discardDownloadCache(f, 0, int64(n+1)*int64(len(buf)))
		}
		if n+1 == size/len(buf)/2 {
			halfway = time.Now()
		}
		if progress != nil && ((n+1)%64 == 0 || n+1 == size/len(buf)) {
			progress((n + 1) * len(buf))
		}
	}
	if err := f.Sync(); err != nil {
		return writeMeasurement{}, err
	}
	end := time.Now()
	first := halfway.Sub(start).Seconds()
	second := end.Sub(halfway).Seconds()
	elapsed := end.Sub(start).Seconds()
	if halfway.IsZero() || first <= 0 || second <= 0 || elapsed <= 0 {
		return writeMeasurement{}, errors.New("Could not measure test duration")
	}
	return writeMeasurement{overall: float64(size) / elapsed, first: float64(size/2) / first, second: float64(size/2) / second}, nil
}

// The mixed test is bounded. It cannot identify SMR:
// small tests may fit entirely inside a drive's cache.
func benchmarkMixed(ctx context.Context, folder string) (float64, error) {
	return benchmarkMixedWithOps(ctx, folder, 8, freeSpaceReserve)
}

func benchmarkMixedWithOps(ctx context.Context, folder string, ops int, reserve uint64) (float64, error) {
	info, err := directoryIdentity(folder)
	if err != nil {
		return 0, errors.New("The selected folder is unavailable")
	}
	free, err := availableSpace(folder)
	if err != nil {
		return 0, err
	}
	if !spaceSufficient(free, uint64(3*ops*64*1024), reserve) {
		return 0, errors.New("Not enough space for the mixed-access test")
	}
	const workers, block = 3, 64 * 1024
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := os.CreateTemp(folder, ".offline-library-mixed-*.tmp")
			if err != nil {
				errs <- err
				return
			}
			name := f.Name()
			defer func() {
				f.Close()
				current, e := directoryIdentity(folder)
				if e == nil && os.SameFile(info, current) {
					os.Remove(name)
				}
			}()
			data := make([]byte, block)
			if _, err = rand.Read(data); err != nil {
				errs <- err
				return
			}
			read := make([]byte, block)
			for n := 0; n < ops; n++ {
				if err = ctx.Err(); err != nil {
					errs <- err
					return
				}
				current, e := directoryIdentity(folder)
				if e != nil || !os.SameFile(info, current) {
					errs <- errors.New("Selected drive changed during the test")
					return
				}
				offset := int64(((n * 5) % ops) * block)
				if _, err = f.WriteAt(data, offset); err != nil {
					errs <- err
					return
				}
				if n%16 == 15 || n == ops-1 {
					if err = f.Sync(); err != nil {
						errs <- err
						return
					}
					discardDownloadCache(f, 0, int64(ops*block))
				}
				if _, err = f.ReadAt(read, offset); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return 0, err
		}
	}
	seconds := time.Since(start).Seconds()
	if seconds <= 0 {
		return 0, errors.New("Could not measure mixed access")
	}
	return float64(workers*ops*block*2) / seconds, nil
}
