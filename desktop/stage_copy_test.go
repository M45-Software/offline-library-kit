package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagedCopyChecksFilesAndDoesNotReplace(t *testing.T) {
	root := t.TempDir()
	dest := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	if e := os.MkdirAll(dir, 0755); e != nil {
		t.Fatal(e)
	}
	source := filepath.Join(dir, "fixture.zim")
	if e := os.WriteFile(source, []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	a := &app{root: root}
	destDir := filepath.Join(dest, "content", "zim")
	if e := os.MkdirAll(destDir, 0755); e != nil {
		t.Fatal(e)
	}
	check := func() error { return nil }
	if e := a.copyOneStaged(context.Background(), source, destDir, check, 2); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(dest, "content", "zim", "fixture.zim")
	b, e := os.ReadFile(target)
	if e != nil || string(b) != "fixture" {
		t.Fatalf("copy %q %v", b, e)
	}
	if e := a.copyOneStaged(context.Background(), source, destDir, check, 2); e != nil {
		t.Fatalf("verified repeat: %v", e)
	}
	if e := os.WriteFile(target, []byte("different"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := a.copyOneStaged(context.Background(), source, destDir, check, 2); e == nil || !strings.Contains(e.Error(), "not replaced") {
		t.Fatalf("conflict: %v", e)
	}
	b, _ = os.ReadFile(target)
	if string(b) != "different" {
		t.Fatal("overwrote destination")
	}
	b, _ = os.ReadFile(source)
	if string(b) != "fixture" {
		t.Fatal("changed staging source")
	}
	if e := a.copyStagedArchives(context.Background(), root, 2, false); e == nil {
		t.Fatal("accepted staging folder as destination")
	}
	if same, e := sameFilesystem(root, dest); e == nil && same {
		if e := a.copyStagedArchives(context.Background(), dest, 2, false); e == nil || !strings.Contains(e.Error(), "different drives") {
			t.Fatalf("same drive accepted: %v", e)
		}
	}
}
func TestStagedCopyCanceledLeavesNoArchive(t *testing.T) {
	root := t.TempDir()
	dest := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "fixture.zim"), []byte("fixture"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &app{root: root}
	if e := a.copyStagedArchives(ctx, dest, 2, false); e == nil {
		t.Fatal("accepted cancellation")
	}
	if _, e := os.Stat(filepath.Join(dest, "content", "zim", "fixture.zim")); !os.IsNotExist(e) {
		t.Fatal("copied after cancellation")
	}
}
