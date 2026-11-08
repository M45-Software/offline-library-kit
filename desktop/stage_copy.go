package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Copy finished local archives to a selected destination without replacing files.
func (a *app) addStageCopyHandler(mux *http.ServeMux) {
	mux.HandleFunc("/api/stage-validate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Destination string `json:"destination"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil || !filepath.IsAbs(req.Destination) {
			http.Error(w, "Choose a destination folder", 400)
			return
		}
		if err := a.checkLibraryRoot(); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		source, err := filepath.EvalSymlinks(a.root)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		target, err := filepath.EvalSymlinks(req.Destination)
		if err != nil {
			http.Error(w, "Destination folder is unavailable", 409)
			return
		}
		if pathWithin(target, source) || pathWithin(source, target) {
			http.Error(w, "Choose a destination outside the staging library", 409)
			return
		}
		if _, err = directoryIdentity(target); err != nil {
			http.Error(w, "Destination is not an accessible folder", 409)
			return
		}
		free, err := availableSpace(target)
		if err != nil {
			http.Error(w, "Cannot check destination free space", 409)
			return
		}
		archiveDir := filepath.Join(target, "content", "zim")
		entries, readErr := os.ReadDir(archiveDir)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			http.Error(w, "Cannot inspect destination collection folder", 409)
			return
		}
		hasArchives := false
		for _, entry := range entries {
			if strings.HasSuffix(strings.ToLower(entry.Name()), ".zim") {
				hasArchives = true
				break
			}
		}
		same, known, err := sharesStorage(source, target)
		if err != nil {
			http.Error(w, "Cannot determine whether staging and destination use different drives", 409)
			return
		}
		writeJSON(w, map[string]any{"freeBytes": free, "hasArchives": hasArchives, "sameFilesystem": same, "physicalCheckAvailable": known})
	})
	mux.HandleFunc("/api/stage-copy", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Destination        string `json:"destination"`
			ReserveGiB         int    `json:"reserveGiB"`
			ConfirmedDifferent bool   `json:"confirmedDifferent"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil || !filepath.IsAbs(req.Destination) || req.ReserveGiB < 2 || req.ReserveGiB > maxReserveGiB {
			http.Error(w, "Choose an absolute destination folder", 400)
			return
		}
		if err := a.checkLibraryRoot(); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		a.mu.Lock()
		if a.closing || a.queueActive || activeDownload(a.current.State) {
			a.mu.Unlock()
			http.Error(w, "Wait for the current download or copy to finish", 409)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.cancelDownload = cancel
		a.retryResolve = nil
		a.lastDownload = nil
		a.current = job{State: "checking", Name: "Preparing destination"}
		a.mu.Unlock()
		go func() {
			defer cancel()
			err := a.copyStagedArchives(ctx, req.Destination, req.ReserveGiB, req.ConfirmedDifferent)
			a.mu.Lock()
			defer a.mu.Unlock()
			a.cancelDownload = nil
			if err == nil {
				a.current = job{State: "complete", Name: "Staged collections copied"}
			} else if errors.Is(err, context.Canceled) {
				a.current = job{State: "canceled", Error: "Copy stopped. Completed destination files and staged files were kept."}
			} else {
				a.current = job{State: "failed", Error: err.Error()}
			}
		}()
		writeJSON(w, map[string]bool{"started": true})
	})
}
func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
func (a *app) copyStagedArchives(ctx context.Context, destination string, reserveGiB int, confirmedDifferent bool) error {
	sourceRoot, err := filepath.EvalSymlinks(a.root)
	if err != nil {
		return err
	}
	targetRoot, err := filepath.EvalSymlinks(destination)
	if err != nil {
		return fmt.Errorf("Destination unavailable: %w", err)
	}
	if pathWithin(targetRoot, sourceRoot) || pathWithin(sourceRoot, targetRoot) {
		return errors.New("Choose a destination outside the staging library")
	}
	same, known, err := sharesStorage(sourceRoot, targetRoot)
	if err != nil {
		return errors.New("Cannot determine whether staging and destination use different drives")
	}
	if same {
		return errors.New("Staging and destination must be on different drives; choose direct writing or a different drive")
	}
	if !known && !confirmedDifferent {
		return errors.New("Confirm that staging and destination are on different physical drives")
	}

	targetInfo, err := directoryIdentity(targetRoot)
	if err != nil {
		return err
	}
	dir := targetRoot
	for _, component := range []string{"content", "zim"} {
		dir = filepath.Join(dir, component)
		if err := os.Mkdir(dir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Destination content folder is not a normal directory")
		}
	}
	dirInfo, err := directoryIdentity(dir)
	if err != nil {
		return err
	}
	check := func() error {
		if err := a.checkLibraryRoot(); err != nil {
			return err
		}
		t, e := directoryIdentity(targetRoot)
		if e != nil || !os.SameFile(targetInfo, t) {
			return errors.New("Destination drive changed during copy")
		}
		d, e := directoryIdentity(dir)
		if e != nil || !os.SameFile(dirInfo, d) {
			return errors.New("Destination collection folder changed during copy")
		}
		return ctx.Err()
	}
	if err := a.ensureSearchAssets(ctx); err != nil {
		return err
	}
	files, err := libraryArchives(a.root)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("There are no downloaded collections to copy")
	}
	for _, source := range files {
		if err := check(); err != nil {
			return err
		}
		if err := a.copyOneStaged(ctx, source, dir, check, reserveGiB); err != nil {
			return err
		}
	}
	if err := a.copySearchTreeChecked(ctx, a.searchRoot(), filepath.Join(destination, "content", "search"), reserveGiB, check); err != nil {
		return err
	}
	return check()
}
func (a *app) copyOneStaged(ctx context.Context, source, dir string, check func() error, reserveGiB int) error {
	before, err := os.Lstat(source)
	if err != nil || !before.Mode().IsRegular() {
		return fmt.Errorf("Staged collection is not a regular file: %s", filepath.Base(source))
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return errors.New("Staged collection changed before copying")
	}
	name := filepath.Base(source)
	final := filepath.Join(dir, name)
	if existing, err := os.Lstat(final); err == nil {
		if !existing.Mode().IsRegular() || existing.Size() != before.Size() {
			return fmt.Errorf("Destination already contains a different %s; it was not replaced", name)
		}
		target, err := os.Open(final)
		if err != nil {
			return err
		}
		defer target.Close()
		targetOpened, err := target.Stat()
		if err != nil || !os.SameFile(existing, targetOpened) {
			return errors.New("Destination file changed during inspection")
		}
		srcHash, dstHash := sha256.New(), sha256.New()
		buf := make([]byte, 1024*1024)
		for {
			if err := check(); err != nil {
				return err
			}
			n, e := input.Read(buf)
			if n > 0 {
				srcHash.Write(buf[:n])
			}
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
		}
		for {
			if err := check(); err != nil {
				return err
			}
			n, e := target.Read(buf)
			if n > 0 {
				dstHash.Write(buf[:n])
			}
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
		}
		srcAfter, e1 := input.Stat()
		dstAfter, e2 := target.Stat()
		sourcePath, e3 := os.Lstat(source)
		targetPath, e4 := os.Lstat(final)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || !os.SameFile(before, sourcePath) || !os.SameFile(existing, targetPath) || !os.SameFile(before, srcAfter) || !os.SameFile(existing, dstAfter) || srcAfter.Size() != before.Size() || dstAfter.Size() != existing.Size() || !srcAfter.ModTime().Equal(before.ModTime()) || !dstAfter.ModTime().Equal(existing.ModTime()) || string(srcHash.Sum(nil)) != string(dstHash.Sum(nil)) {
			return fmt.Errorf("Destination already contains a different %s; it was not replaced", name)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	free, err := availableSpace(dir)
	if err != nil {
		return err
	}
	if !spaceSufficient(free, uint64(before.Size()), uint64(reserveGiB)*gibibyte) {
		return fmt.Errorf("Not enough destination space for %s and the %d GiB reserve", name, reserveGiB)
	}
	tempFile, err := os.CreateTemp(dir, ".offline-library-copy-*.part")
	if err != nil {
		return err
	}
	temp := tempFile.Name()
	tempInfo, err := tempFile.Stat()
	if err != nil {
		tempFile.Close()
		os.Remove(temp)
		return err
	}
	dirInfo, err := directoryIdentity(dir)
	if err != nil {
		tempFile.Close()
		return err
	}
	defer func() {
		currentDir, e := directoryIdentity(dir)
		currentFile, f := os.Lstat(temp)
		if e == nil && f == nil && os.SameFile(dirInfo, currentDir) && os.SameFile(tempInfo, currentFile) {
			os.Remove(temp)
		}
	}()
	defer tempFile.Close()
	buf := make([]byte, 1024*1024)
	h := sha256.New()
	var done int64
	for {
		if err := check(); err != nil {
			return err
		}
		n, e := input.Read(buf)
		if n > 0 {
			done += int64(n)
			if done > before.Size() {
				return errors.New("Staged file changed during copying")
			}
			if err := a.checkProcessingSpace(uint64(n)); err != nil {
				return err
			}
			free, spaceErr := availableSpace(dir)
			if spaceErr != nil {
				return spaceErr
			}
			if !spaceSufficient(free, uint64(n), uint64(reserveGiB)*gibibyte) {
				return errors.New("Copy stopped to protect the destination free-space reserve")
			}
			if _, err := tempFile.Write(buf[:n]); err != nil {
				return err
			}
			h.Write(buf[:n])
			if strings.EqualFold(filepath.Ext(name), ".zim") {
				a.setJob(job{State: "downloading", Name: "Copying " + name, Done: done, Total: before.Size()})
			} else {
				a.setJob(job{State: "indexing", Name: "Preparing search", Done: done, Total: before.Size()})
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	after, err := input.Stat()
	sourcePath, pathErr := os.Lstat(source)
	if err != nil || pathErr != nil || !os.SameFile(before, sourcePath) || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || done != before.Size() {
		return errors.New("Staged file changed during copying")
	}
	if err := tempFile.Sync(); err != nil {
		return err
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	verify := sha256.New()
	var checked int64
	for {
		if err := check(); err != nil {
			return err
		}
		n, e := tempFile.Read(buf)
		if n > 0 {
			checked += int64(n)
			verify.Write(buf[:n])
			if strings.EqualFold(filepath.Ext(name), ".zim") {
				a.setJob(job{State: "verifying", Name: "Copied " + name, Done: checked, Total: before.Size()})
			} else {
				a.setJob(job{State: "indexing", Name: "Preparing search", Done: checked, Total: before.Size()})
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	if checked != done || string(h.Sum(nil)) != string(verify.Sum(nil)) {
		return fmt.Errorf("Copied file failed verification: %s", name)
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	if err := installExclusive(temp, final); err != nil {
		return fmt.Errorf("Could not install %s without replacing a file: %w", name, err)
	}
	return nil
}
