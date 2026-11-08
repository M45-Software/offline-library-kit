package main

import (
	"context"
	"os"
	"testing"
)

func TestBenchmarkRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	rate, err := benchmarkFolder(context.Background(), dir)
	if err != nil || rate <= 0 {
		t.Fatalf("benchmark: %v %v", rate, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("benchmark left files: %v %v", entries, err)
	}
}
func TestBenchmarkCancellationRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := benchmarkFolder(ctx, dir); err == nil {
		t.Fatal("canceled benchmark succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("benchmark left files: %v %v", entries, err)
	}
}

func TestMixedBenchmarkRemovesTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	rate, err := benchmarkMixed(context.Background(), dir)
	if err != nil || rate <= 0 {
		t.Fatalf("mixed benchmark: %v %v", rate, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("mixed benchmark left files: %v %v", entries, err)
	}
}
func TestMixedBenchmarkCancellationRemovesTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := benchmarkMixed(ctx, dir); err == nil {
		t.Fatal("canceled mixed benchmark succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("mixed benchmark left files: %v %v", entries, err)
	}
}

func TestExtendedBenchmarkCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := benchmarkWrite(ctx, dir, 16*1024*1024*1024, freeSpaceReserve, nil); err == nil {
		t.Fatal("canceled extended write succeeded")
	}
	if _, err := benchmarkMixedWithOps(ctx, dir, 8192, freeSpaceReserve); err == nil {
		t.Fatal("canceled extended mixed test succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("extended benchmark left files: %v %v", entries, err)
	}
}
