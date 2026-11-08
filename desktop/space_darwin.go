//go:build darwin

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
	size := uint64(stat.Bsize)
	if size == 0 || stat.Bavail > math.MaxUint64/size {
		return 0, errors.New("Invalid filesystem space information")
	}
	return stat.Bavail * size, nil
}
