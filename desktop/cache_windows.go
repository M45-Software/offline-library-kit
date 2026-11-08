//go:build windows

package main

import "os"

func prepareDownloadCache(f *os.File)                       {}
func discardDownloadCache(f *os.File, offset, length int64) {}
