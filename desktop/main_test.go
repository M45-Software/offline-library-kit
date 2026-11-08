package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDownloadVerifiedAndExistingPreserved(t *testing.T) {
	payload := []byte("verified archive fixture")
	sum := sha256.Sum256(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client()}
	req := downloadRequest{URL: server.URL, Name: "fixture.zim", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(payload))}
	if err := a.download(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(a.root, "content", "zim", req.Name)
	b, err := os.ReadFile(p)
	if err != nil || string(b) != string(payload) {
		t.Fatalf("bad installed file: %v", err)
	}
	if err = a.download(context.Background(), req); err == nil {
		t.Fatal("existing destination was overwritten")
	}
	b, _ = os.ReadFile(p)
	if string(b) != string(payload) {
		t.Fatal("existing file changed")
	}
}
func TestDownloadMismatchRetainsPartial(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("abc")) }))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client()}
	req := downloadRequest{URL: server.URL, Name: "fixture.zim", SHA256: strings.Repeat("0", 64), Bytes: 3}
	if err := a.download(context.Background(), req); err == nil {
		t.Fatal("mismatch accepted")
	}
	dir := filepath.Join(a.root, "content", "zim")
	if _, err := os.Stat(filepath.Join(dir, req.Name)); !os.IsNotExist(err) {
		t.Fatal("unverified file installed")
	}
	if _, err := os.Stat(filepath.Join(dir, req.Name+".part")); err != nil {
		t.Fatal("partial not retained")
	}
}
func TestRequestValidationAndLocalAPIAuthorization(t *testing.T) {
	for _, n := range []string{"../escape.zim", `dir\escape.zim`, "not-a-zim.txt"} {
		if validRequest(downloadRequest{Name: n, URL: "https://example.com/a", Bytes: 1, SHA256: strings.Repeat("a", 64)}) == nil {
			t.Fatalf("accepted %q", n)
		}
	}
	a := &app{root: t.TempDir(), token: "secret"}
	h := a.handler()
	r := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/state", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("missing token accepted")
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1:1234/api/state", nil)
	r.Header.Set("X-Library-Token", "secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("valid request rejected: %v", w.Code)
	}
}
func TestCatalogMustMatchSelectedFiles(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "content", "zim", "a.zim")
	catalog := filepath.Join(root, "library.xml")
	if err := os.WriteFile(catalog, []byte(`<library><book path="content/zim/a.zim"/></library>`), 0600); err != nil {
		t.Fatal(err)
	}
	if !catalogMatches(catalog, root, []string{f}) {
		t.Fatal("matching catalog rejected")
	}
	if catalogMatches(catalog, root, []string{f, filepath.Join(root, "content", "zim", "b.zim")}) {
		t.Fatal("stale catalog accepted")
	}
	if err := os.WriteFile(catalog, []byte(`<library><book path="../../outside.zim"/></library>`), 0600); err != nil {
		t.Fatal(err)
	}
	if catalogMatches(catalog, root, []string{f}) {
		t.Fatal("unexpected path accepted")
	}
}

func TestLibraryFolderWithBracketsFindsOnlyItsArchives(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library [family]")
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.zim", "two.ZIM", "unfinished.ZIM.part"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{root: root, token: "secret"}
	files, err := libraryArchives(root)
	if err != nil || len(files) != 2 || filepath.Base(files[0]) != "one.zim" || filepath.Base(files[1]) != "two.ZIM" {
		t.Fatalf("incorrect archives: %v, %v", files, err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/state", nil)
	r.Header.Set("X-Library-Token", "secret")
	a.handler().ServeHTTP(w, r)
	var state struct {
		ArchiveCount int `json:"archiveCount"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.ArchiveCount != 2 {
		t.Fatalf("incorrect state: %d %s", w.Code, w.Body)
	}
	if _, err := a.startReader(); err == nil || !strings.Contains(err.Error(), "Kiwix reader is missing") {
		t.Fatalf("reader failed to discover archives: %v", err)
	}
}

func TestUnreadableCollectionFolderIsNotReportedAsEmpty(t *testing.T) {
	root := t.TempDir()
	a := &app{root: root, token: "secret"}
	if files, err := libraryArchives(root); err != nil || len(files) != 0 {
		t.Fatalf("new library should be empty: %v %v", files, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "content"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "content", "zim"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/state", nil)
	r.Header.Set("X-Library-Token", "secret")
	a.handler().ServeHTTP(w, r)
	var state struct {
		Error string `json:"libraryError"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &state) != nil || !strings.Contains(state.Error, "Cannot read collection folder") {
		t.Fatalf("missing folder error: %d %s", w.Code, w.Body)
	}
	if _, err := a.startReader(); err == nil || !strings.Contains(err.Error(), "Cannot read collection folder") {
		t.Fatalf("reader hid folder error: %v", err)
	}
}

func TestResumeAfterInterruptedDownload(t *testing.T) {
	payload := []byte("verified archive fixture for resume")
	sum := sha256.Sum256(payload)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Write(payload[:10])
			return
		}
		if r.Header.Get("Range") != "bytes=10-" {
			t.Errorf("wrong range: %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 10-%d/%d", len(payload)-1, len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(payload[10:])
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client()}
	req := downloadRequest{URL: server.URL, Name: "resume.zim", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(payload))}
	if err := a.download(context.Background(), req); err == nil {
		t.Fatal("interrupted download accepted")
	}
	if err := a.download(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(a.root, "content", "zim", req.Name)
	got, err := os.ReadFile(p)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("bad resumed file: %q %v", got, err)
	}
	if _, err = os.Stat(p + ".part.json"); !os.IsNotExist(err) {
		t.Fatal("resume info not removed")
	}
}
func TestResumeRejectsIgnoredRangeAndChangedMetadata(t *testing.T) {
	payload := []byte("some bytes")
	sum := sha256.Sum256(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload[:4]) }))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client()}
	req := downloadRequest{URL: server.URL, Name: "partial.zim", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(payload))}
	if a.download(context.Background(), req) == nil {
		t.Fatal("short download accepted")
	}
	p := filepath.Join(a.root, "content", "zim", req.Name)
	before, _ := os.ReadFile(p + ".part")
	if err := a.download(context.Background(), req); err == nil || !strings.Contains(err.Error(), "does not support resuming") {
		t.Fatalf("range not rejected: %v", err)
	}
	after, _ := os.ReadFile(p + ".part")
	if string(before) != string(after) {
		t.Fatal("partial was overwritten")
	}
	req.SHA256 = strings.Repeat("a", 64)
	if err := a.download(context.Background(), req); err == nil || !strings.Contains(err.Error(), "different download") {
		t.Fatalf("metadata change not rejected: %v", err)
	}
}
func TestDownloadLockPreventsConcurrentWriters(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockDownload(filepath.Join(dir, "locked.zim.part.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	a := &app{root: root, client: http.DefaultClient}
	err = a.download(context.Background(), downloadRequest{URL: "https://example.com/a", Name: "locked.zim", Bytes: 1, SHA256: strings.Repeat("a", 64)})
	if err == nil || !strings.Contains(err.Error(), "Another app") {
		t.Fatalf("lock failed: %v", err)
	}
}
func TestCancelAndResume(t *testing.T) {
	payload := []byte("cancel and resume fixture")
	sum := sha256.Sum256(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			w.Write(payload[:6])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Range") != "bytes=6-" {
			t.Errorf("unexpected range %s", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 6-%d/%d", len(payload)-1, len(payload)))
		w.WriteHeader(206)
		w.Write(payload[6:])
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), token: "secret", client: server.Client()}
	item := downloadRequest{URL: server.URL, Name: "cancel.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(sum[:])}
	resolve := func(context.Context) (downloadRequest, error) { return item, nil }
	if !a.beginDownload(job{State: "checking"}, resolve) {
		t.Fatal("did not start")
	}
	part := filepath.Join(a.root, "content", "zim", item.Name+".part")
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := os.Stat(part)
		if err == nil && s.Size() == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("download did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:1234/api/download/cancel", nil)
	r.Header.Set("X-Library-Token", "secret")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("cancel: %d", w.Code)
	}
	for {
		a.mu.Lock()
		state := a.current.State
		a.mu.Unlock()
		if state == "canceled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancel did not finish: %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !a.beginDownload(job{State: "checking"}, resolve) {
		t.Fatal("could not resume")
	}
	for {
		a.mu.Lock()
		state := a.current.State
		msg := a.current.Error
		a.mu.Unlock()
		if state == "complete" {
			break
		}
		if state == "failed" || time.Now().After(deadline) {
			t.Fatalf("resume failed %s: %s", state, msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := os.ReadFile(strings.TrimSuffix(part, ".part"))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("bad output %q: %v", got, err)
	}
}
func TestInstallDoesNotReplaceExistingFile(t *testing.T) {
	dir := t.TempDir()
	partial := filepath.Join(dir, "data.part")
	final := filepath.Join(dir, "data.zim")
	if err := os.WriteFile(partial, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(final, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installExclusive(partial, final); err == nil {
		t.Fatal("replaced an existing file")
	}
	got, _ := os.ReadFile(final)
	if string(got) != "existing" {
		t.Fatal("existing data changed")
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatal("partial was not retained")
	}
}
func TestSpaceReserveAndNoPartialWhenInsufficient(t *testing.T) {
	for _, tc := range []struct {
		free, remaining uint64
		want            bool
	}{{0, 0, true}, {freeSpaceReserve, 0, true}, {freeSpaceReserve, 1, false}, {freeSpaceReserve + 100, 100, true}, {freeSpaceReserve + 100, 101, false}} {
		if got := spaceSufficient(tc.free, tc.remaining, freeSpaceReserve); got != tc.want {
			t.Fatalf("spaceSufficient(%d,%d) = %v", tc.free, tc.remaining, got)
		}
	}
	root := t.TempDir()
	a := &app{root: root, client: http.DefaultClient}
	req := downloadRequest{URL: "https://example.com/too-big.zim", Name: "huge.zim", Bytes: math.MaxInt64, SHA256: strings.Repeat("a", 64)}
	err := a.download(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "Not enough free space") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "content", "zim", "huge.zim.part")); !os.IsNotExist(err) {
		t.Fatal("insufficient space created a partial")
	}
}

func TestDownloadSpacePathUsesNearestExistingDirectory(t *testing.T) {
	root := t.TempDir()
	if got, err := downloadSpacePath(root); err != nil || got != root {
		t.Fatalf("missing content: %q %v", got, err)
	}
	content := filepath.Join(root, "content")
	if err := os.Mkdir(content, 0755); err != nil {
		t.Fatal(err)
	}
	if got, err := downloadSpacePath(root); err != nil || got != content {
		t.Fatalf("missing zim: %q %v", got, err)
	}
	zim := filepath.Join(content, "zim")
	if err := os.Mkdir(zim, 0755); err != nil {
		t.Fatal(err)
	}
	if got, err := downloadSpacePath(root); err != nil || got != zim {
		t.Fatalf("existing zim: %q %v", got, err)
	}
	if err := os.Remove(zim); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zim, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := downloadSpacePath(root); err == nil {
		t.Fatal("accepted a file as collection directory")
	}
}

func TestStateReportsCollectionFilesystemSpace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses a separate Linux tmpfs")
	}
	target, err := os.MkdirTemp("/dev/shm", "offline-library-space-")
	if err != nil {
		t.Skipf("separate tmpfs unavailable: %v", err)
	}
	defer os.Remove(target)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "content"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "content", "zim")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	a := &app{root: root, token: "secret"}
	req := httptest.NewRequest("GET", "http://127.0.0.1:1234/api/state", nil)
	req.Header.Set("X-Library-Token", "secret")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, req)
	var state struct {
		FreeBytes uint64 `json:"freeBytes"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil {
		t.Fatalf("invalid state: %d %s", w.Code, w.Body)
	}
	free, err := availableSpace(target)
	if err != nil {
		t.Fatal(err)
	}
	const tolerance = 128 * 1024 * 1024
	if state.FreeBytes > free+tolerance || free > state.FreeBytes+tolerance {
		t.Fatalf("reported %d bytes; collection filesystem has %d", state.FreeBytes, free)
	}
}
func TestResumeMetadataDoesNotStoreURLSecrets(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client()}
	req := downloadRequest{URL: server.URL + "/?token=private-fixture", Name: "partial.zim", Bytes: 10, SHA256: strings.Repeat("a", 64)}
	if a.download(context.Background(), req) == nil {
		t.Fatal("short response accepted")
	}
	p := filepath.Join(a.root, "content", "zim", req.Name+".part.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private-fixture") || strings.Contains(string(b), server.URL) {
		t.Fatal("resume file stores a URL")
	}
}
func TestLegacyResumeMetadataIsMigrated(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("short")) }))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client()}
	req := downloadRequest{URL: server.URL + "/?token=legacy-secret", Name: "old.zim", Bytes: 20, SHA256: strings.Repeat("a", 64)}
	if a.download(context.Background(), req) == nil {
		t.Fatal("short response accepted")
	}
	p := filepath.Join(a.root, "content", "zim", req.Name+".part.json")
	old, _ := json.Marshal(req)
	if err := os.WriteFile(p, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.download(context.Background(), req); err == nil || !strings.Contains(err.Error(), "does not support resuming") {
		t.Fatalf("unexpected retry error %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "legacy-secret") || !strings.Contains(string(b), "url_sha256") {
		t.Fatalf("metadata not migrated: %s", b)
	}
}
func TestDownloadRejectsChangedDestinationDirectory(t *testing.T) {
	payload := []byte("destination fixture")
	sum := sha256.Sum256(payload)
	root := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	moved := filepath.Join(root, "content", "old-zim")
	changeResult := make(chan error, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.Rename(dir, moved); err != nil {
			changeResult <- err
			http.Error(w, "Cannot replace open directory", http.StatusConflict)
			return
		}
		if err := os.Mkdir(dir, 0755); err != nil {
			changeResult <- err
			http.Error(w, "Cannot make replacement directory", http.StatusConflict)
			return
		}
		changeResult <- nil
		w.Write(payload)
	}))
	defer server.Close()
	a := &app{root: root, client: server.Client()}
	req := downloadRequest{URL: server.URL, Name: "fixture.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(sum[:])}
	err := a.download(context.Background(), req)
	if changeErr := <-changeResult; changeErr != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows prevented renaming an open directory: %v", changeErr)
		}
		t.Fatalf("Could not create replacement directory: %v", changeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "destination changed") {
		t.Fatalf("unexpected error %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, req.Name)); !os.IsNotExist(err) {
		t.Fatal("installed to replacement directory")
	}
	if _, err = os.Stat(filepath.Join(moved, req.Name+".part")); err != nil {
		t.Fatal("original partial not retained")
	}
}
func TestDownloadAcrossCacheWindows(t *testing.T) {
	chunk := bytes.Repeat([]byte("verified streaming data!"), 32768)
	const repeats = 86
	h := sha256.New()
	for i := 0; i < repeats; i++ {
		h.Write(chunk)
	}
	expected := hex.EncodeToString(h.Sum(nil))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < repeats; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client()}
	req := downloadRequest{URL: server.URL, Name: "large.zim", Bytes: int64(len(chunk) * repeats), SHA256: expected}
	if err := a.download(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(a.root, "content", "zim", req.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h.Reset()
	if _, err = io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		t.Fatal("content differs across cache windows")
	}
}

func TestReplacedLibraryRootIsNeverUsedForDownload(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "library")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	identity, err := directoryIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{root: root, rootIdentity: identity, client: http.DefaultClient}
	if err := os.Rename(root, filepath.Join(parent, "old-library")); err != nil {
		t.Fatal(err)
	}
	item := downloadRequest{URL: "https://example.com/file", Name: "test.zim", Bytes: 1, SHA256: strings.Repeat("a", 64)}
	if err := a.download(context.Background(), item); err == nil {
		t.Fatal("download accepted missing library")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("missing library was recreated")
	}
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := a.download(context.Background(), item); err == nil {
		t.Fatal("download accepted replacement library")
	}
	if _, err := os.Stat(filepath.Join(root, "content")); !os.IsNotExist(err) {
		t.Fatal("wrote into replacement library")
	}
	if _, err := a.startReader(); err == nil {
		t.Fatal("reader accepted replacement library")
	}
}
