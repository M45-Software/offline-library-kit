//go:build linux

package main

import (
	"bufio"
	"os"
	"strings"
)

func mountedDrivePaths() ([]string, error) {
	f, e := os.Open("/proc/self/mountinfo")
	if e != nil {
		return nil, e
	}
	defer f.Close()
	paths := []string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 6 {
			continue
		}
		p := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, "\\").Replace(fields[4])
		if p == "/" || p == "/mnt" || p == "/media" || p == "/run/media" || strings.HasPrefix(p, "/media/") || strings.HasPrefix(p, "/run/media/") || strings.HasPrefix(p, "/mnt/") {
			paths = append(paths, p)
		}
	}
	return paths, sc.Err()
}
