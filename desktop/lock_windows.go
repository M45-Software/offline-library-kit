//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

func lockDownload(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	kernel := syscall.NewLazyDLL("kernel32.dll")
	lock := kernel.NewProc("LockFileEx")
	unlock := kernel.NewProc("UnlockFileEx")
	var overlap syscall.Overlapped
	ok, _, e := lock.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlap)))
	if ok == 0 {
		f.Close()
		return nil, e
	}
	return func() { _, _, _ = unlock.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlap))); _ = f.Close() }, nil
}
