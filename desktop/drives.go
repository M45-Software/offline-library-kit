package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

type driveOption struct {
	Path      string `json:"path"`
	Label     string `json:"label"`
	FreeBytes uint64 `json:"freeBytes"`
}

type stagingOption struct {
	driveOption
	ReserveGiB             int  `json:"reserveGiB"`
	PhysicalCheckAvailable bool `json:"physicalCheckAvailable"`
}

// Only suggest separate storage. Unknown physical identity still requires the
// operator's existing confirmation when selecting the staging destination.
func stagingOptions(root string, drives []driveOption) []stagingOption {
	candidates := append([]driveOption(nil), drives...)
	home, err := os.UserHomeDir()
	if err == nil {
		if free, e := availableSpace(home); e == nil {
			candidates = append([]driveOption{{home, "This computer", free}}, candidates...)
		}
	}
	out := []stagingOption{}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate.Path] {
			continue
		}
		seen[candidate.Path] = true
		same, known, e := sharesStorage(root, candidate.Path)
		if e != nil || same {
			continue
		}
		reserve := 2
		if candidate.Path == home {
			reserve = 32
		}
		out = append(out, stagingOption{candidate, reserve, known})
	}
	return out
}

func driveOptions() ([]driveOption, error) {
	paths, err := mountedDrivePaths()
	if err != nil {
		return nil, err
	}
	out := make([]driveOption, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		info, e := os.Stat(path)
		if e != nil || !info.IsDir() {
			continue
		}
		free, e := availableSpace(path)
		if e != nil {
			continue
		}
		label := filepath.Base(path)
		if path == string(filepath.Separator) {
			label = "Computer"
		}
		out = append(out, driveOption{path, label, free})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
func (a *app) addDriveHandler(mux *http.ServeMux) {
	mux.HandleFunc("/api/drives", func(w http.ResponseWriter, r *http.Request) {
		drives, err := driveOptions()
		if err != nil {
			http.Error(w, "Could not list mounted drives", 500)
			return
		}
		writeJSON(w, map[string]any{"drives": drives, "stagingOptions": stagingOptions(a.root, drives), "libraryRoot": a.root})
	})
}

// A shared filesystem always means shared storage. On Linux, inspect backing
// block devices as well, including partitions and common device-mapper stacks.
func sharesStorage(a, b string) (shared, physicalKnown bool, err error) {
	same, err := sameFilesystem(a, b)
	if err != nil {
		return false, false, err
	}
	if same {
		return true, true, nil
	}
	same, err = samePhysicalDisk(a, b)
	if err != nil {
		return false, false, nil
	}
	return same, true, nil
}
