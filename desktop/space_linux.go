//go:build linux

package main

import (
	"errors"
	"golang.org/x/sys/unix"
	"math"
)

func availableSpace(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	size := stat.Frsize
	if size <= 0 {
		size = stat.Bsize
	}
	if size <= 0 || stat.Bavail > math.MaxUint64/uint64(size) {
		return 0, errors.New("Invalid filesystem space information")
	}
	return stat.Bavail * uint64(size), nil
}
