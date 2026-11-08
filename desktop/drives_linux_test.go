//go:build linux

package main

import (
	"path/filepath"
	"testing"
)

func TestMountedDriveOptions(t *testing.T) {
	drives, err := driveOptions()
	if err != nil {
		t.Fatal(err)
	}
	root := false
	for _, d := range drives {
		if !filepath.IsAbs(d.Path) {
			t.Fatalf("relative drive: %q", d.Path)
		}
		if d.Path == "/" {
			root = true
		}
	}
	if !root {
		t.Fatal("system filesystem not listed")
	}
}
