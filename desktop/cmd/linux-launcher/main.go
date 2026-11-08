//go:build linux

// The Linux entry point uses native Chromium sandboxing and, when available,
// a compatible, already-installed system SUID helper. It changes no host policy.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var systemHelpers = []string{"/opt/google/chrome/chrome-sandbox", "/usr/lib/chromium/chrome-sandbox", "/usr/lib/chromium-browser/chrome-sandbox"}

func trustedHelper(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSetuid == 0 || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return false
	}
	// Do not trust a system helper reached through a user-writable directory.
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	for dir := filepath.Dir(real); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return false
		}
		if dir == "/" {
			break
		}
	}
	return true
}
func helperAPI(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, path, "--get-api").Output()
	return strings.TrimSpace(string(data)), err
}
func prepareSandbox(dir string, candidates []string, trusted func(string) bool, api func(string) (string, error)) error {
	helper := filepath.Join(dir, "chrome-sandbox")
	backup := helper + ".bundled"
	if trusted(helper) {
		return nil
	}
	original := helper
	if _, err := os.Lstat(backup); err == nil {
		original = backup
	}
	expected, err := api(original)
	if err != nil || expected == "" {
		return nil
	}
	for _, candidate := range candidates {
		if !trusted(candidate) {
			continue
		}
		version, err := api(candidate)
		if err != nil || version != expected {
			continue
		}
		if original == helper {
			if info, err := os.Lstat(helper); err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("cannot preserve bundled sandbox helper")
			}
			if err := os.Link(helper, backup); err != nil && !os.IsExist(err) {
				return err
			}
		} else if info, err := os.Lstat(helper); err == nil && info.Mode().IsRegular() {
			saved, err := os.Stat(backup)
			if err != nil || !os.SameFile(info, saved) {
				return fmt.Errorf("sandbox helper changed; refusing to replace it")
			}
		}
		file, err := os.CreateTemp(dir, ".sandbox-link-*")
		if err != nil {
			return err
		}
		name := file.Name()
		file.Close()
		os.Remove(name)
		defer os.Remove(name)
		if err := os.Symlink(candidate, name); err != nil {
			return err
		}
		return os.Rename(name, helper)
	}
	// Hosts with working unprivileged namespaces need no privileged helper.
	// If neither mechanism is available, Chromium will fail closed.
	return nil
}
func main() {
	if os.Getuid() == 0 {
		fmt.Fprintln(os.Stderr, "Run Offline Library as a regular user.")
		os.Exit(1)
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dir := filepath.Dir(self)
	binary := filepath.Join(dir, "offline-library-bin")
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		fmt.Fprintln(os.Stderr, "Offline Library is incomplete. Keep the whole app folder together.")
		os.Exit(1)
	}
	if err := prepareSandbox(dir, systemHelpers, trustedHelper, helperAPI); err != nil {
		fmt.Fprintln(os.Stderr, "Could not prepare the sandbox:", err)
		os.Exit(1)
	}
	if err := syscall.Exec(binary, append([]string{binary}, os.Args[1:]...), os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "Could not start Offline Library:", err)
		os.Exit(1)
	}
}
