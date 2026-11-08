//go:build linux || darwin

package main

import (
	"errors"
	"os"
	"syscall"
)

func sameFilesystem(a, b string) (bool, error) {
	x, e := os.Stat(a)
	if e != nil {
		return false, e
	}
	y, e := os.Stat(b)
	if e != nil {
		return false, e
	}
	xs, ok := x.Sys().(*syscall.Stat_t)
	if !ok {
		return false, errors.New("filesystem identity unavailable")
	}
	ys, ok := y.Sys().(*syscall.Stat_t)
	if !ok {
		return false, errors.New("filesystem identity unavailable")
	}
	return xs.Dev == ys.Dev, nil
}
