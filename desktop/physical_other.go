//go:build windows || darwin

package main

import "errors"

func samePhysicalDisk(a, b string) (bool, error) {
	return false, errors.New("physical disk identity unavailable")
}
