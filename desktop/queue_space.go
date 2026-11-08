package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type spaceReservation struct{ remaining, extra uint64 }
type queueSpaceBudget struct {
	mu           sync.Mutex
	root         string
	reserve      uint64
	reservations map[*app]spaceReservation
	changed      chan struct{}
	space        func(string) (uint64, error)
}

func newQueueSpaceBudget(root string, reserve int) *queueSpaceBudget {
	return &queueSpaceBudget{root: root, reserve: uint64(reserve) * gibibyte, reservations: map[*app]spaceReservation{}, changed: make(chan struct{}), space: availableSpace}
}
func (b *queueSpaceBudget) notify() { close(b.changed); b.changed = make(chan struct{}) }
func (b *queueSpaceBudget) acquire(ctx context.Context, worker *app, remaining, extra uint64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.mu.Lock()
		free, err := b.space(b.root)
		required := remaining
		others := 0
		add := func(n uint64) {
			if n > ^uint64(0)-required {
				err = errors.New("Collection size is too large")
			} else {
				required += n
			}
		}
		add(extra)
		for key, reservation := range b.reservations {
			if key != worker {
				others++
				add(reservation.remaining)
				add(reservation.extra)
			}
		}
		if err == nil && spaceSufficient(free, required, b.reserve) {
			b.reservations[worker] = spaceReservation{remaining, extra}
			b.notify()
			b.mu.Unlock()
			return nil
		}
		changed := b.changed
		b.mu.Unlock()
		if err != nil {
			return err
		}
		if others == 0 {
			return errors.New("Not enough free space for this collection and preparation workspace after the reserve")
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (b *queueSpaceBudget) release(worker *app) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.reservations, worker)
	b.notify()
}
func (a *app) writeDownloadChunk(ctx context.Context, file *os.File, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.downloadBudget == nil {
		n, err := file.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		return err
	}
	b := a.downloadBudget
	b.mu.Lock()
	defer b.mu.Unlock()
	free, err := b.space(b.root)
	if err != nil {
		return err
	}
	if !spaceSufficient(free, uint64(len(data))+(32<<20), b.reserve) {
		return errors.New("Download stopped to protect your free-space reserve; partial retained")
	}
	n, err := file.Write(data)
	reservation := b.reservations[a]
	reservation.remaining -= min(reservation.remaining, uint64(n))
	b.reservations[a] = reservation
	b.notify()
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}
func (a *app) checkProcessingSpace(next uint64) error {
	if a.downloadBudget == nil {
		return nil
	}
	b := a.downloadBudget
	b.mu.Lock()
	defer b.mu.Unlock()
	required := max(next, uint64(32<<20))
	for _, reservation := range b.reservations {
		if reservation.remaining > ^uint64(0)-required {
			return errors.New("Collection size is too large")
		}
		required += reservation.remaining
	}
	free, err := b.space(b.root)
	if err != nil {
		return err
	}
	if !spaceSufficient(free, required, b.reserve) {
		return errors.New("Preparation stopped to protect space reserved for active downloads and your free-space reserve")
	}
	return nil
}
func (a *app) collectionExtraSpace(item downloadRequest) (uint64, error) {
	if item.Bytes <= 0 || uint64(item.Bytes) > ^uint64(0)/4 {
		return 0, errors.New("Collection size is too large")
	}
	var extra uint64
	add := func(n uint64) error {
		if n > ^uint64(0)-extra {
			return errors.New("Collection size is too large")
		}
		extra += n
		return nil
	}
	if isIFixit(item.Name) && !completedRepair(filepath.Join(a.root, "content", "zim"), item.Name) {
		size := uint64(item.Bytes)
		if err := add(size + max(uint64(128<<20), size/4)); err != nil {
			return 0, err
		}
	}
	if a.searchDir != "" {
		if !searchTreeReady(a.searchDir, a.searchRoot()) {
			if err := add(searchPackAllowance); err != nil {
				return 0, err
			}
		}
		if customSearchFamily(item.Name) {
			if err := add(collectionSearchWorkAllowance(item.Name, item.Bytes)); err != nil {
				return 0, err
			}
		}
	}
	return extra, nil
}
func (a *app) reserveInstalledPreparation(ctx context.Context, item downloadRequest) error {
	extra, err := a.collectionExtraSpace(item)
	if err != nil {
		return err
	}
	a.setJob(job{State: "waiting", Name: item.Name, Error: "Waiting for preparation space…"})
	if err = a.downloadBudget.acquire(ctx, a, 0, extra); err != nil {
		return fmt.Errorf("Cannot prepare collection: %w", err)
	}
	return nil
}
