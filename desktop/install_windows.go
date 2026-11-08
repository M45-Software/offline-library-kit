//go:build windows

package main

import "syscall"

func installExclusive(part, final string) error {
	source, err := syscall.UTF16PtrFromString(part)
	if err != nil {
		return err
	}
	destination, err := syscall.UTF16PtrFromString(final)
	if err != nil {
		return err
	}
	// MoveFile fails if the target already exists; it does not replace it.
	return syscall.MoveFile(source, destination)
}
