package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const queueFilename = ".offline-library-download-list.json"
const queueLockFilename = ".offline-library-download-list.lock"

const maxQueueRecordBytes = 256 * 1024

type queueRecord struct {
	BandwidthKiB int                    `json:"bandwidthKiB,omitempty"`
	History      []downloadHistoryEntry `json:"history,omitempty"`
	StallSeconds int                    `json:"stallSeconds,omitempty"`
	Concurrency  int                    `json:"concurrency,omitempty"`
	Version      int                    `json:"version"`
	Items        []queuedCollection     `json:"items"`
	ReserveGiB   int                    `json:"reserveGiB,omitempty"`
}
type queueStore struct {
	exists   bool
	checksum [32]byte
}

// Read only a recognizable, bounded regular file. Compare the opened file
// identity to the directory entry before allowing a replacement.
func readQueueRecord(path string) (queueRecord, bool, [32]byte, error) {
	var zero [32]byte
	var record queueRecord
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return record, false, zero, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return record, false, zero, errors.New("Download list file is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return record, false, zero, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return record, false, zero, errors.New("Download list file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxQueueRecordBytes+1))
	if err != nil || len(data) > maxQueueRecordBytes {
		return record, false, zero, errors.New("Download list file is too large or unreadable")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || (record.Concurrency != 0 && (record.Concurrency < 1 || record.Concurrency > maxDownloadConcurrency)) || (record.StallSeconds != 0 && (record.StallSeconds < minStallSeconds || record.StallSeconds > maxStallSeconds)) || record.BandwidthKiB < 0 || record.BandwidthKiB > maxBandwidthKiB || len(record.History) > maxDownloadHistory || record.Version != 1 || record.Items == nil || len(record.Items) > 50 || (record.ReserveGiB != 0 && (record.ReserveGiB < 2 || record.ReserveGiB > maxReserveGiB)) {
		return record, false, zero, errors.New("Download list file is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return record, false, zero, errors.New("Download list file has extra data")
	}
	for _, entry := range record.History {
		if entry.Title == "" || len(entry.Title) > 800 || entry.State != "complete" && entry.State != "failed" || entry.Time.IsZero() || entry.Retries < 0 || entry.Retries > 6 || entry.HTTPStatus < 0 || entry.HTTPStatus > 599 || len(entry.Detail) > 1000 || entry.ID != "" && !uuidPattern.MatchString(entry.ID) {
			return record, false, zero, errors.New("Download history is invalid")
		}
	}
	seen := make(map[string]bool)
	for i, item := range record.Items {
		id := strings.ToLower(item.ID)
		if !uuidPattern.MatchString(item.ID) || seen[id] || item.Title == "" || len(item.Title) > 200 || strings.TrimSpace(item.Title) != item.Title || item.ApproxBytes < 0 || item.ApproxBytes > maxQueueEstimate || item.NetworkFailure && !item.Paused {
			return record, false, zero, errors.New("Download list file contains invalid items")
		}
		seen[id] = true
		record.Items[i].ID = id
	}
	return record, true, sha256.Sum256(data), nil
}

func (a *app) lockQueueStore() (func(), error) {
	if err := a.checkLibraryRoot(); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(a.root, queueLockFilename)
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("Download list lock is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	unlock, err := lockDownload(lockPath)
	if err != nil {
		return nil, fmt.Errorf("Cannot lock download list: %w", err)
	}
	if err = a.checkLibraryRoot(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

func (a *app) initializeQueueStore() error {
	unlock, err := a.lockQueueStore()
	if err != nil {
		return err
	}
	defer unlock()
	record, exists, checksum, err := readQueueRecord(filepath.Join(a.root, queueFilename))
	if err != nil {
		return err
	}
	a.queueStore = &queueStore{exists: exists, checksum: checksum}
	if exists {
		a.catalogQueue = record.Items
		a.reserveGiB = record.ReserveGiB
		a.downloadConcurrency = record.Concurrency
		a.stallSeconds = record.StallSeconds
		a.bandwidthKiB = record.BandwidthKiB
		a.downloadHistory = record.History
	}
	return nil
}

// Caller holds a.mu. Refuse to start a stale list if another app changed it.
func (a *app) validateQueueLocked() error {
	if a.queueStoreError != "" {
		return errors.New(a.queueStoreError)
	}
	if a.queueStore == nil {
		return errors.New("Download list is not initialized")
	}
	unlock, err := a.lockQueueStore()
	if err != nil {
		return err
	}
	defer unlock()
	_, exists, checksum, err := readQueueRecord(filepath.Join(a.root, queueFilename))
	if err != nil {
		return err
	}
	if exists != a.queueStore.exists || (exists && checksum != a.queueStore.checksum) {
		return errors.New("Download list changed outside this app; restart to review it")
	}
	return nil
}

// Caller holds a.mu. Every in-flight item remains on disk until installation
// and preparation complete, so an interruption restores all unfinished work.
func (a *app) saveQueueLocked(current *queuedCollection, pending []queuedCollection) error {
	if a.queueStoreError != "" {
		return errors.New(a.queueStoreError)
	}
	if a.queueStore == nil {
		return errors.New("Download list is not initialized")
	}
	unlock, err := a.lockQueueStore()
	if err != nil {
		return err
	}
	defer unlock()
	items := make([]queuedCollection, 0, len(pending)+1)
	if len(a.queueInFlight) > 0 {
		items = append(items, a.queueInFlight...)
	} else if current != nil {
		items = append(items, *current)
	}
	items = append(items, pending...)
	data, err := json.Marshal(queueRecord{Version: 1, Items: items, ReserveGiB: a.reserveGiB, Concurrency: a.downloadConcurrency, StallSeconds: a.stallSeconds, BandwidthKiB: a.bandwidthKiB, History: a.downloadHistory})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxQueueRecordBytes {
		return errors.New("Download list and history are too large")
	}
	path := filepath.Join(a.root, queueFilename)
	_, exists, checksum, err := readQueueRecord(path)
	if err != nil {
		return err
	}
	if exists != a.queueStore.exists || (exists && checksum != a.queueStore.checksum) {
		return errors.New("Download list changed outside this app; restart to review it")
	}
	temp, err := os.CreateTemp(a.root, ".offline-library-list-*.part")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = a.checkLibraryRoot(); err != nil {
		return err
	}
	_, nowExists, nowChecksum, err := readQueueRecord(path)
	if err != nil {
		return err
	}
	if nowExists != exists || (nowExists && nowChecksum != checksum) {
		return errors.New("Download list changed outside this app; restart to review it")
	}
	if !exists {
		if err = installExclusive(name, path); err != nil {
			return fmt.Errorf("Cannot create download list: %w", err)
		}
	} else if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("Cannot update download list: %w", err)
	}
	a.queueStore.exists = true
	a.queueStore.checksum = sha256.Sum256(data)
	return nil
}
