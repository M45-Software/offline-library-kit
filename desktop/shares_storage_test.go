package main

import "testing"

func TestSameFilesystemCannotBeStagingDestination(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	same, known, err := sharesStorage(a, b)
	if err != nil || !known || !same {
		t.Fatalf("expected shared storage: %v %v %v", same, known, err)
	}
}
