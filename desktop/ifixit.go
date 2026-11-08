package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	repair "offline-library-kit/desktop/internal/ifixitrepair"
)

const repairedSuffix = ".local-repaired-v1.zim"

type repairStamp struct {
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	UUID     string `json:"uuid"`
	Checksum string `json:"checksum"`
}

type repairReceipt struct {
	Version int           `json:"version"`
	Source  repairStamp   `json:"source"`
	Output  repairStamp   `json:"output"`
	Report  repair.Report `json:"report"`
}

func isIFixit(name string) bool {
	name = strings.ToLower(name)
	return filepath.Base(name) == name && strings.HasSuffix(name, ".zim") && !strings.HasSuffix(name, repairedSuffix) && (name == "ifixit.zim" || strings.HasPrefix(name, "ifixit_") || strings.HasPrefix(name, "ifixit-"))
}

func (a *app) checkIFixitDownloadSpace(item downloadRequest) error {
	if !isIFixit(item.Name) && a.searchDir == "" {
		return nil
	}
	spacePath, err := downloadSpacePath(a.root)
	if err != nil {
		return err
	}
	free, err := availableSpace(spacePath)
	if err != nil {
		return errors.New("Cannot check space for iFixit")
	}
	size := uint64(item.Bytes)
	if size > ^uint64(0)/4 {
		return errors.New("Collection size is too large")
	}
	needed := size
	if isIFixit(item.Name) {
		needed += size + max(uint64(128<<20), size/4)
	}
	if a.searchDir != "" {
		if !searchTreeReady(a.searchDir, a.searchRoot()) {
			needed += searchPackAllowance
		}
		if customSearchFamily(item.Name) {
			workspace := collectionSearchWorkAllowance(item.Name, item.Bytes)
			if workspace > ^uint64(0)-needed {
				return errors.New("Collection size is too large")
			}
			needed += workspace
		}
	}
	if matchingPartial(a.root, item) {
		if part, err := os.Stat(filepath.Join(a.root, "content", "zim", item.Name+".part")); err == nil && part.Size() >= 0 && uint64(part.Size()) <= size {
			needed -= uint64(part.Size())
		}
	}
	a.mu.Lock()
	reserve := uint64(a.effectiveReserveGiB()) * gibibyte
	a.mu.Unlock()
	if !spaceSufficient(free, needed, reserve) {
		return errors.New("Not enough space for iFixit and link repair. Choose fewer collections or another drive")
	}
	return nil
}

func repairedName(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name)) + repairedSuffix
}

// Small, bounded identity checks for the completion receipt. Full readback is
// done before installation; scanning the library never hashes gigabytes.
func stampRepairArchive(name string) (repairStamp, error) {
	var stamp repairStamp
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return stamp, errors.New("Archive is missing or is not a regular file")
	}
	f, err := os.Open(name)
	if err != nil {
		return stamp, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return stamp, errors.New("Archive changed while opening")
	}
	header := make([]byte, 80)
	if _, err = io.ReadFull(f, header); err != nil {
		return stamp, err
	}
	checksumAt := binary.LittleEndian.Uint64(header[72:])
	if binary.LittleEndian.Uint32(header) != 72173914 || info.Size() < 96 || checksumAt != uint64(info.Size()-16) {
		return stamp, errors.New("Invalid ZIM header or size")
	}
	sum := make([]byte, 16)
	if _, err = f.ReadAt(sum, int64(checksumAt)); err != nil {
		return stamp, err
	}
	return repairStamp{info.Size(), info.ModTime().UnixNano(), hex.EncodeToString(header[8:24]), hex.EncodeToString(sum)}, nil
}

func completedRepair(dir, source string) bool {
	if filepath.Base(source) != source || !isIFixit(source) {
		return false
	}
	f, err := os.Open(filepath.Join(dir, source+".repair.json"))
	if err != nil {
		return false
	}
	defer f.Close()
	var receipt repairReceipt
	if json.NewDecoder(io.LimitReader(f, 16<<10)).Decode(&receipt) != nil || receipt.Version != 1 {
		return false
	}
	before, err := stampRepairArchive(filepath.Join(dir, source))
	if err != nil || before != receipt.Source {
		return false
	}
	after, err := stampRepairArchive(filepath.Join(dir, repairedName(source)))
	return err == nil && after == receipt.Output && after.Checksum == receipt.Report.Checksum
}

// Obtain the publisher original before creating a separate local adaptation.
// Retrying a stopped repair verifies the original instead of downloading again.
func (a *app) obtainCollection(ctx context.Context, item downloadRequest) error {
	installed, err := a.matchesInstalledArchive(ctx, item)
	if err != nil {
		return err
	}
	if !installed {
		if err = a.downloadWithRetries(ctx, item); err != nil {
			return err
		}
	}
	if err = a.repairIFixit(ctx, item); err != nil {
		return err
	}
	return a.prepareCollectionSearch(ctx, item)
}

func (a *app) repairIFixit(ctx context.Context, item downloadRequest) error {
	if !isIFixit(item.Name) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.checkLibraryRoot(); err != nil {
		return err
	}
	dir := filepath.Join(a.root, "content", "zim")
	dirInfo, err := directoryIdentity(dir)
	if err != nil {
		return err
	}
	checkDirectory := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.checkLibraryRoot(); err != nil {
			return err
		}
		now, err := directoryIdentity(dir)
		if err != nil || !os.SameFile(dirInfo, now) {
			return errors.New("Library drive changed during repair; original retained")
		}
		return nil
	}
	unlock, err := lockDownload(filepath.Join(dir, item.Name+".part.lock"))
	if err != nil {
		return errors.New("Another app is using this collection; original retained")
	}
	defer unlock()
	if completedRepair(dir, item.Name) {
		return nil
	}
	source := filepath.Join(dir, item.Name)
	before, err := stampRepairArchive(source)
	if err != nil {
		return err
	}
	final := filepath.Join(dir, repairedName(item.Name))
	for _, name := range []string{final, source + ".repair.json"} {
		if _, err = os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return errors.New("A previous iFixit repair cannot be confirmed; existing files retained")
		}
	}
	a.mu.Lock()
	reserve := uint64(a.effectiveReserveGiB()) * gibibyte
	a.mu.Unlock()
	free, err := availableSpace(dir)
	if err != nil {
		return errors.New("Cannot check space for link repair; original retained")
	}
	// The source stays intact. Allow a full copy plus replacement HTML and tables.
	needed := uint64(before.Size) + max(uint64(128<<20), uint64(before.Size)/4)
	if !spaceSufficient(free, needed, reserve) {
		return errors.New("Not enough space to repair iFixit links and keep your free-space reserve. The original is safe")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := filepath.Join(dir, ".ifixit-repair-"+hex.EncodeToString(nonce[:])+".part")
	progress := func(p repair.Progress) error {
		start, span := int64(0), int64(0)
		switch p.Phase {
		case "copying":
			span = 250
		case "pages":
			start, span = 250, 550
		case "checksum":
			start, span = 800, 100
		}
		done := start
		if p.Total > 0 {
			done += span * p.Done / p.Total
		}
		a.setJob(job{State: "repairing", Name: item.Name, Done: done, Total: 1000})
		return checkDirectory()
	}
	report, err := repair.Repair(ctx, source, temporary, repair.Options{Workers: 2, Progress: progress, BeforeWrite: func(n int) error {
		if err := a.checkProcessingSpace(uint64(n)); err != nil {
			return err
		}
		if err := checkDirectory(); err != nil {
			return err
		}
		free, err := availableSpace(dir)
		if err != nil || !spaceSufficient(free, uint64(n), reserve) {
			return errors.New("Link repair stopped to protect your free-space reserve; original and unfinished copy retained")
		}
		return nil
	}})
	if err != nil {
		return fmt.Errorf("Link repair stopped; original and any unfinished copy retained: %w", err)
	}
	// Reopen the written file and verify its on-disk checksum independently of
	// the repair writer before publishing it as a usable collection.
	if err = verifyRepairReadback(ctx, temporary, report.Checksum, func(done, total int64) {
		a.setJob(job{State: "repairing", Name: item.Name, Done: 900 + 100*done/total, Total: 1000})
	}); err != nil {
		return err
	}
	after, err := stampRepairArchive(source)
	if err != nil || before != after {
		return errors.New("Original changed during repair; copies retained")
	}
	output, err := stampRepairArchive(temporary)
	if err != nil {
		return err
	}
	if err = checkDirectory(); err != nil {
		return err
	}
	if err = installExclusive(temporary, final); err != nil {
		return fmt.Errorf("Cannot install repaired copy; files retained: %w", err)
	}
	report.Source, report.Output = item.Name, repairedName(item.Name)
	receipt := repairReceipt{Version: 1, Source: before, Output: output, Report: report}
	f, err := os.CreateTemp(dir, ".ifixit-receipt-*.part")
	if err != nil {
		return err
	}
	if err = json.NewEncoder(f).Encode(receipt); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = checkDirectory(); err != nil {
		return err
	}
	if err = installExclusive(f.Name(), source+".repair.json"); err != nil {
		return err
	}
	return nil
}

func verifyRepairReadback(ctx context.Context, name, expected string, progress func(int64, int64)) error {
	stamp, err := stampRepairArchive(name)
	if err != nil {
		return err
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	h := md5.New()
	buf := make([]byte, 1<<20)
	left, done := stamp.Size-16, int64(0)
	for left > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(f, buf[:min(int64(len(buf)), left)])
		if err != nil {
			return err
		}
		h.Write(buf[:n])
		left -= int64(n)
		done += int64(n)
		if progress != nil {
			progress(done, stamp.Size-16)
		}
	}
	want, err := hex.DecodeString(expected)
	if err != nil || !bytes.Equal(h.Sum(nil), want) || stamp.Checksum != expected {
		return errors.New("Repaired copy failed readback verification; original and unfinished copy retained")
	}
	current, err := stampRepairArchive(name)
	if err != nil || current != stamp {
		return errors.New("Repaired copy changed during verification; files retained")
	}
	currentInfo, err := os.Lstat(name)
	if err != nil || !os.SameFile(opened, currentInfo) {
		return errors.New("Repaired copy was replaced during verification; files retained")
	}
	return nil
}
