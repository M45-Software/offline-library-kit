//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompatibleSystemHelperPreservesBundledOriginal(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "chrome-sandbox")
	candidate := filepath.Join(t.TempDir(), "system-helper")
	os.WriteFile(helper, []byte("bundled"), 0755)
	os.WriteFile(candidate, []byte("system"), 0755)
	trusted := func(path string) bool {
		resolved, err := filepath.EvalSymlinks(path)
		return err == nil && resolved == candidate
	}
	if err := prepareSandbox(dir, []string{candidate}, trusted, func(string) (string, error) { return "1", nil }); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(helper)
	if err != nil || target != candidate {
		t.Fatal(target, err)
	}
	data, err := os.ReadFile(helper + ".bundled")
	if err != nil || string(data) != "bundled" {
		t.Fatal("lost original")
	}
	if err := prepareSandbox(dir, []string{candidate}, trusted, func(string) (string, error) { t.Fatal("reconfigured valid helper"); return "", nil }); err != nil {
		t.Fatal(err)
	}
}
func TestUntrustedOrIncompatibleHelperIsNotUsed(t *testing.T) {
	for _, trust := range []bool{false, true} {
		dir := t.TempDir()
		helper := filepath.Join(dir, "chrome-sandbox")
		os.WriteFile(helper, []byte("original"), 0755)
		err := prepareSandbox(dir, []string{"candidate"}, func(path string) bool { return path == "candidate" && trust }, func(path string) (string, error) {
			if path == helper {
				return "1", nil
			}
			return "2", nil
		})
		data, _ := os.ReadFile(helper)
		if err != nil || string(data) != "original" {
			t.Fatal("replaced helper", err)
		}
	}
}
func TestUserOwnedHelperIsNeverTrusted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper")
	os.WriteFile(path, []byte("helper"), 0755)
	if trustedHelper(path) {
		t.Fatal("trusted user helper")
	}
}
