//go:build darwin

package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func prepareDownloadCache(f *os.File)                       { _, _ = unix.FcntlInt(f.Fd(), unix.F_NOCACHE, 1) }
func discardDownloadCache(f *os.File, offset, length int64) {}
