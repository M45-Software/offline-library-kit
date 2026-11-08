//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"strings"
)

func volumePath(path string) (string, error) {
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return "", e
	}
	buf := make([]uint16, 32768)
	if e = windows.GetVolumePathName(p, &buf[0], uint32(len(buf))); e != nil {
		return "", e
	}
	return windows.UTF16ToString(buf), nil
}
func sameFilesystem(a, b string) (bool, error) {
	x, e := volumePath(a)
	if e != nil {
		return false, e
	}
	y, e := volumePath(b)
	if e != nil {
		return false, e
	}
	return strings.EqualFold(x, y), nil
}
