//go:build linux

package main

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func installExclusive(part, final string) error {
	err := unix.Renameat2(unix.AT_FDCWD, part, unix.AT_FDCWD, final, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return installByLink(part, final)
	}
	return err
}
func installByLink(part, final string) error {
	if err := os.Link(part, final); err != nil {
		return err
	}
	return os.Remove(part)
}
