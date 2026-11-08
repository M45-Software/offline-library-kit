//go:build linux

package main

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func backingDisks(path string) (map[string]bool, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return nil, err
	}
	link := fmt.Sprintf("/sys/dev/block/%d:%d", unix.Major(uint64(st.Dev)), unix.Minor(uint64(st.Dev)))
	root, err := filepath.EvalSymlinks(link)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(node string) error {
		resolved, err := filepath.EvalSymlinks(node)
		if err != nil {
			return err
		}
		if visited[resolved] {
			return nil
		}
		visited[resolved] = true
		if _, err := os.Stat(filepath.Join(resolved, "partition")); err == nil {
			resolved = filepath.Dir(resolved)
		}
		entries, err := os.ReadDir(filepath.Join(resolved, "slaves"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if len(entries) == 0 {
			found[resolved] = true
			return nil
		}
		for _, entry := range entries {
			if err := visit(filepath.Join(resolved, "slaves", entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("physical disk identity unavailable")
	}
	return found, nil
}
func samePhysicalDisk(a, b string) (bool, error) {
	x, e := backingDisks(a)
	if e != nil {
		return false, e
	}
	y, e := backingDisks(b)
	if e != nil {
		return false, e
	}
	for disk := range x {
		if y[disk] {
			return true, nil
		}
	}
	return false, nil
}
