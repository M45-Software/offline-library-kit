package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func readResumeInfo(path string) (resumeRecord, error) {
	var record resumeRecord
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return record, errors.New("Resume information is unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return record, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(data) > 8192 {
		return record, errors.New("Invalid resume information")
	}
	if err = json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	return record, nil
}
func matchesResumeInfo(saved resumeRecord, r downloadRequest) bool {
	expected := resumeInfo(r)
	return saved.Name == r.Name && saved.Bytes == r.Bytes && strings.EqualFold(saved.SHA256, r.SHA256) && (saved.URLHash == expected.URLHash || (saved.URLHash == "" && saved.LegacyURL == r.URL))
}
func matchingPartial(root string, r downloadRequest) bool {
	if validRequest(r) != nil {
		return false
	}
	part := filepath.Join(root, "content", "zim", r.Name+".part")
	info, err := os.Lstat(part)
	if err != nil || !info.Mode().IsRegular() || info.Size() > r.Bytes {
		return false
	}
	saved, err := readResumeInfo(part + ".json")
	return err == nil && matchesResumeInfo(saved, r)
}
func discardPartial(root string, r downloadRequest) error {
	if validRequest(r) != nil {
		return errors.New("Invalid saved download")
	}
	dir := filepath.Join(root, "content", "zim")
	original, err := directoryIdentity(dir)
	if err != nil {
		return err
	}
	part := filepath.Join(dir, r.Name+".part")
	unlock, err := lockDownload(part + ".lock")
	if err != nil {
		return fmt.Errorf("Another app is using this download: %w", err)
	}
	defer unlock()
	current, err := directoryIdentity(dir)
	if err != nil || !os.SameFile(original, current) {
		return errors.New("Library destination changed; nothing was discarded")
	}
	if !matchingPartial(root, r) {
		return errors.New("Partial download cannot be safely identified; inspect it manually")
	}
	if err = os.Remove(part); err != nil {
		return err
	}
	if err = os.Remove(part + ".json"); err != nil {
		return fmt.Errorf("Partial removed, but resume information remains: %w", err)
	}
	return nil
}

// savedPartial contains only safe display data. In particular, legacy URLs and
// publisher hashes are never returned to the control panel.
type savedPartial struct {
	Name        string `json:"name"`
	Done        int64  `json:"done"`
	Total       int64  `json:"total"`
	CanResume   bool   `json:"canResume"`
	Queued      bool   `json:"queued,omitempty"`
	Fingerprint string `json:"fingerprint"`
	CatalogID   string `json:"-"`
}

func savedPartialByName(root, name string) (savedPartial, bool) {
	var result savedPartial
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return result, false
	}
	part := filepath.Join(root, "content", "zim", name+".part")
	info, err := os.Lstat(part)
	if err != nil || !info.Mode().IsRegular() {
		return result, false
	}
	record, err := readResumeInfo(part + ".json")
	if err != nil || record.Name != name {
		return result, false
	}
	// Validate all fields used to recognize a partial, without exposing URLs.
	probe := downloadRequest{URL: "https://example.invalid/", Name: record.Name, Bytes: record.Bytes, SHA256: record.SHA256}
	if validRequest(probe) != nil || info.Size() > record.Bytes {
		return result, false
	}
	if record.URLHash == "" {
		probe.URL = record.LegacyURL
		if validRequest(probe) != nil {
			return result, false
		}
	} else if len(record.URLHash) != 64 {
		return result, false
	} else if _, err := hex.DecodeString(record.URLHash); err != nil {
		return result, false
	}
	// Include the saved metadata and file state so a stale UI cannot delete a
	// different partial that later reused this name. Do not expose the fields.
	encoded, err := json.Marshal(record)
	if err != nil {
		return result, false
	}
	f, err := os.Open(part)
	if err != nil {
		return result, false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, false
	}
	h := sha256.New()
	h.Write(encoded)
	fmt.Fprintf(h, "\x00%d\x00%d", info.Size(), info.ModTime().UnixNano())
	// Bounded samples detect common same-size edits without hashing a large archive.
	buffer := make([]byte, 1024)
	for _, offset := range []int64{0, max(int64(0), info.Size()-int64(len(buffer)))} {
		n, readErr := f.ReadAt(buffer, offset)
		if readErr != nil && readErr != io.EOF {
			return result, false
		}
		h.Write(buffer[:n])
	}
	result = savedPartial{Name: name, Done: info.Size(), Total: record.Bytes, CanResume: uuidPattern.MatchString(record.CatalogID), CatalogID: record.CatalogID, Fingerprint: hex.EncodeToString(h.Sum(nil))}
	return result, true
}

func listSavedPartials(root string) []savedPartial {
	result := make([]savedPartial, 0)
	dir := filepath.Join(root, "content", "zim")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".zim.part") {
			continue
		}
		if item, ok := savedPartialByName(root, strings.TrimSuffix(name, ".part")); ok {
			result = append(result, item)
		}
	}
	return result
}

// discardSavedPartial only removes the exact recognized partial the user saw.
func discardSavedPartial(root, name, fingerprint string) error {
	if _, ok := savedPartialByName(root, name); !ok {
		return errors.New("Partial download cannot be safely identified")
	}
	dir := filepath.Join(root, "content", "zim")
	original, err := directoryIdentity(dir)
	if err != nil {
		return err
	}
	part := filepath.Join(dir, name+".part")
	unlock, err := lockDownload(part + ".lock")
	if err != nil {
		return fmt.Errorf("Another app is using this download: %w", err)
	}
	defer unlock()
	current, err := directoryIdentity(dir)
	if err != nil || !os.SameFile(original, current) {
		return errors.New("Library destination changed; nothing was discarded")
	}
	saved, ok := savedPartialByName(root, name)
	if !ok || fingerprint == "" || saved.Fingerprint != fingerprint {
		return errors.New("Partial download changed; refresh the list and try again")
	}
	if err = os.Remove(part); err != nil {
		return err
	}
	if err = os.Remove(part + ".json"); err != nil {
		return fmt.Errorf("Partial removed, but resume information remains: %w", err)
	}
	return nil
}
