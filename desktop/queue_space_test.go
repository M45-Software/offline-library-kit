package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueueBudgetDoesNotOvercommitConcurrentDownloads(t *testing.T) {
	budget := newQueueSpaceBudget(t.TempDir(), 0)
	budget.space = func(string) (uint64, error) { return 1 << 30, nil }
	first, second := &app{}, &app{}
	if err := budget.acquire(context.Background(), first, 600<<20, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := budget.acquire(ctx, second, 600<<20, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("overcommitted: %v", err)
	}
	budget.release(first)
	if err := budget.acquire(context.Background(), second, 600<<20, 0); err != nil {
		t.Fatal(err)
	}
}
func TestProcessingCannotConsumeActiveDownloadReservations(t *testing.T) {
	budget := newQueueSpaceBudget(t.TempDir(), 0)
	budget.reserve = 128 << 20
	budget.space = func(string) (uint64, error) { return 512 << 20, nil }
	downloading := &app{}
	processing := &app{downloadBudget: budget}
	budget.reservations[downloading] = spaceReservation{remaining: 400 << 20}
	if err := processing.checkProcessingSpace(1); err == nil {
		t.Fatal("preparation consumed reserved download space")
	}
	budget.release(downloading)
	if err := processing.checkProcessingSpace(1); err != nil {
		t.Fatal(err)
	}
}
func TestDownloadWriteStopsBeforeReserve(t *testing.T) {
	root := t.TempDir()
	budget := newQueueSpaceBudget(root, 2)
	budget.space = func(string) (uint64, error) { return budget.reserve + (32 << 20) + 3, nil }
	a := &app{downloadBudget: budget}
	budget.reservations[a] = spaceReservation{remaining: 4}
	file, err := os.Create(filepath.Join(root, "partial"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err = a.writeDownloadChunk(context.Background(), file, []byte("four")); err == nil {
		t.Fatal("write crossed reserve margin")
	}
	info, _ := file.Stat()
	if info.Size() != 0 || budget.reservations[a].remaining != 4 {
		t.Fatal("unsafe write changed partial or reservation")
	}
}
