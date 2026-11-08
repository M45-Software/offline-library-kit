//go:build linux

package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func prepareDownloadCache(f *os.File) {}
func discardDownloadCache(f *os.File, offset, length int64) {
	_ = unix.Fadvise(int(f.Fd()), offset, length, unix.FADV_DONTNEED)
}
