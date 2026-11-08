//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
)

func mountedDrivePaths() ([]string, error) {
	mask, e := windows.GetLogicalDrives()
	if e != nil {
		return nil, e
	}
	paths := []string{}
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		p := fmt.Sprintf("%c:\\", 'A'+i)
		name, e := windows.UTF16PtrFromString(p)
		if e != nil {
			continue
		}
		kind := windows.GetDriveType(name)
		if kind == 2 || kind == 3 || kind == 4 {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
