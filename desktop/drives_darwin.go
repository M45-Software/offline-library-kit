//go:build darwin

package main

import (
	"bytes"
	"golang.org/x/sys/unix"
	"strings"
)

func mountedDrivePaths() ([]string, error) {
	n, e := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if e != nil {
		return nil, e
	}
	if n > 4096 {
		n = 4096
	}
	stats := make([]unix.Statfs_t, n)
	n, e = unix.Getfsstat(stats, unix.MNT_NOWAIT)
	if e != nil {
		return nil, e
	}
	if n > len(stats) {
		n = len(stats)
	}
	paths := []string{}
	for _, st := range stats[:n] {
		raw := st.Mntonname[:]
		if i := bytes.IndexByte(raw, 0); i >= 0 {
			raw = raw[:i]
		}
		p := string(raw)
		if p == "/" || strings.HasPrefix(p, "/Volumes/") {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
