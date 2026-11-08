//go:build darwin

package main

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func installExclusive(part, final string) error {
	err := unix.RenamexNp(part, final, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
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
