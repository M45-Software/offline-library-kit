package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	librarysearch "offline-library-kit/desktop/internal/librarysearch"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html search-space.json
var assets embed.FS

type downloadRequest struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	CatalogID string `json:"-"`
}
type resumeRecord struct {
	Name      string `json:"name"`
	URLHash   string `json:"url_sha256,omitempty"`
	LegacyURL string `json:"url,omitempty"`
	CatalogID string `json:"catalog_id,omitempty"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
}

func resumeInfo(r downloadRequest) resumeRecord {
	h := sha256.Sum256([]byte(r.URL))
	return resumeRecord{Name: r.Name, URLHash: hex.EncodeToString(h[:]), SHA256: r.SHA256, Bytes: r.Bytes, CatalogID: r.CatalogID}
}

type job struct {
	BytesPerSecond float64 `json:"bytesPerSecond,omitempty"`
	ETASeconds     *int64  `json:"etaSeconds,omitempty"`
	SpeedMeasured  bool    `json:"speedMeasured,omitempty"`
	transfer       *transferMeter
	State          string `json:"state"`
	Name           string `json:"name,omitempty"`
	Done           int64  `json:"done"`
	Total          int64  `json:"total"`
	Error          string `json:"error,omitempty"`
}
type app struct {
	bandwidthKiB        int
	bandwidth           *downloadLimiter
	downloadHistory     []downloadHistoryEntry
	retryCount          int
	lastHTTPStatus      int
	activeBudget        *queueSpaceBudget
	queueID             string
	searchDir           string
	searchMu            sync.Mutex
	searchEngine        *librarysearch.Library
	mu                  sync.Mutex
	root, reader, token string
	rootIdentity        os.FileInfo
	readerCmd           *exec.Cmd
	readerURL           string
	homeURL             string
	homeWarning         string
	current             job
	client              *http.Client
	shutdown            func()
	closing             bool
	cancelDownload      context.CancelFunc
	lastDownload        *downloadRequest
	retryResolve        func(context.Context) (downloadRequest, error)
	retryInitial        job
	catalogQueue        []queuedCollection
	queueCurrent        *queuedCollection
	queueActive         bool
	downloadConcurrency int
	stallSeconds        int
	stallTimeout        time.Duration
	queueInFlight       []queuedCollection
	queueItemCancels    map[string]context.CancelFunc
	queueWake           chan struct{}
	queueScope          map[string]bool
	queueContext        context.Context
	queueResolve        queueResolver
	queueJobs           map[string]job
	jobSink             func(job)
	settingsValidator   func() error
	downloadBudget      *queueSpaceBudget
	queueStore          *queueStore
	queueStoreError     string
	reserveGiB          int
}

func (a *app) setJob(j job) {
	a.mu.Lock()
	a.current = j
	sink := a.jobSink
	a.mu.Unlock()
	if sink != nil {
		sink(j)
	}
}
func validRequest(r downloadRequest) error {
	if r.Name == "" || r.Name == "." || r.Name == ".." || filepath.Base(r.Name) != r.Name || strings.ContainsAny(r.Name, `/\`) || !strings.HasSuffix(strings.ToLower(r.Name), ".zim") {
		return errors.New("Enter a plain .zim filename")
	}
	u, e := url.Parse(r.URL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("Use an HTTPS download URL")
	}
	if len(r.SHA256) != 64 {
		return errors.New("Enter a 64-character SHA-256")
	}
	if _, e = hex.DecodeString(r.SHA256); e != nil {
		return errors.New("Enter a valid SHA-256")
	}
	if r.Bytes <= 0 {
		return errors.New("Enter the expected byte size")
	}
	return nil
}

// Snapshot identity from an open handle. On Windows, os.Stat can defer
// resolving a file ID until SameFile is called, after the path has changed.
func directoryIdentity(path string) (os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("Not a directory")
	}
	return info, nil
}
func (a *app) checkLibraryRoot() error {
	info, err := directoryIdentity(a.root)
	if err != nil || (a.rootIdentity != nil && !os.SameFile(a.rootIdentity, info)) {
		return errors.New("Library folder is missing or has changed. Reconnect it or choose the folder again")
	}
	return nil
}

// Report the filesystem that would receive new archives without creating any
// directories. Existing mount points and symlinks can use a different drive.
func downloadSpacePath(root string) (string, error) {
	current := root
	for _, component := range []string{"content", "zim"} {
		next := filepath.Join(current, component)
		info, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			info, err = os.Stat(next)
			if err != nil {
				return "", err
			}
		}
		if !info.IsDir() {
			return "", errors.New("Collection destination is not a directory")
		}
		current = next
	}
	return current, nil
}

func (a *app) download(ctx context.Context, r downloadRequest) error {
	if err := validRequest(r); err != nil {
		return err
	}
	if err := a.checkIFixitDownloadSpace(r); err != nil {
		return err
	}
	if err := a.checkLibraryRoot(); err != nil {
		return err
	}
	dir := filepath.Join(a.root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	dirInfo, err := directoryIdentity(dir)
	if err != nil {
		return err
	}
	checkDirectory := func() error {
		if err := a.checkLibraryRoot(); err != nil {
			return err
		}
		current, err := directoryIdentity(dir)
		if err != nil || !os.SameFile(dirInfo, current) {
			return errors.New("Library destination changed during the download; partial retained")
		}
		return nil
	}
	final := filepath.Join(dir, r.Name)
	part := final + ".part"
	metadata := part + ".json"
	unlock, err := lockDownload(part + ".lock")
	if err != nil {
		return fmt.Errorf("Another app is using this download: %w", err)
	}
	defer unlock()
	if err := checkDirectory(); err != nil {
		return err
	}
	if _, err = os.Lstat(final); !os.IsNotExist(err) {
		return errors.New("Destination already exists or cannot be inspected")
	}
	existing, err := os.Lstat(part)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var f *os.File
	var offset int64
	if err == nil && existing.Mode().IsRegular() {
		offset = existing.Size()
	}
	if offset > r.Bytes {
		return errors.New("Partial is larger than expected; retained for inspection")
	}
	free, spaceErr := availableSpace(dir)
	if spaceErr != nil {
		return fmt.Errorf("Cannot check available library space: %w", spaceErr)
	}
	a.mu.Lock()
	reserveGiB := a.effectiveReserveGiB()
	limiter := a.limiterLocked()
	var configErr error
	if a.queueStore != nil || a.queueStoreError != "" {
		configErr = a.validateQueueLocked()
	}
	a.mu.Unlock()
	if configErr == nil && a.settingsValidator != nil {
		configErr = a.settingsValidator()
	}
	if configErr != nil {
		return fmt.Errorf("Cannot check download settings: %w", configErr)
	}
	if a.downloadBudget != nil {
		a.setJob(job{State: "waiting", Name: r.Name, Done: offset, Total: r.Bytes})
		extra, e := a.collectionExtraSpace(r)
		if e != nil {
			return e
		}
		if e = a.downloadBudget.acquire(ctx, a, uint64(r.Bytes-offset), extra); e != nil {
			return e
		}
	}
	if !spaceSufficient(free, uint64(r.Bytes-offset), uint64(reserveGiB)*gibibyte) {
		return fmt.Errorf("Not enough free space: need %.2f GB for the download plus a %d GiB reserve", float64(r.Bytes-offset)/1e9, reserveGiB)
	}
	if err == nil {
		if !existing.Mode().IsRegular() {
			return errors.New("Partial download is not a regular file")
		}
		offset = existing.Size()
		if offset > r.Bytes {
			return errors.New("Partial is larger than expected; retained for inspection")
		}
		saved, e := readResumeInfo(metadata)
		if e != nil || !matchesResumeInfo(saved, r) {
			return errors.New("Partial belongs to a different download or has invalid resume information; retained for inspection")
		}
		expected := resumeInfo(r)
		if (saved.URLHash == "" && saved.LegacyURL != "") || (uuidPattern.MatchString(r.CatalogID) && saved.CatalogID != r.CatalogID) {
			if err := replaceResumeInfo(dir, metadata, expected); err != nil {
				return fmt.Errorf("Cannot update resume information: %w", err)
			}
		}
		f, err = os.OpenFile(part, os.O_RDWR, 0600)
	} else {
		f, err = os.OpenFile(part, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		m, e := os.OpenFile(metadata, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			f.Close()
			return fmt.Errorf("Cannot create resume information; partial retained: %w", e)
		}
		e = json.NewEncoder(m).Encode(resumeInfo(r))
		if e == nil {
			e = m.Sync()
		}
		if closeErr := m.Close(); e == nil {
			e = closeErr
		}
		if e != nil {
			f.Close()
			return e
		}
	}
	if err != nil {
		return err
	}
	defer f.Close()
	prepareDownloadCache(f)
	h := sha256.New()
	buf := make([]byte, 1024*1024)
	var checked int64
	a.setJob(job{State: "checking", Name: r.Name, Total: offset})
	var dropped int64
	for checked < offset {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, e := f.Read(buf[:min64(int64(len(buf)), offset-checked)])
		if n > 0 {
			h.Write(buf[:n])
			checked += int64(n)
			if checked-dropped >= 64*1024*1024 {
				discardDownloadCache(f, dropped, checked-dropped)
				dropped = checked
			}
			a.setJob(job{State: "checking", Name: r.Name, Done: checked, Total: offset})
		}
		if e != nil {
			return fmt.Errorf("Cannot read partial: %w", e)
		}
	}
	if offset < r.Bytes {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
		if e != nil {
			return e
		}
		req.Header.Set("Accept-Encoding", "identity")
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		networkCtx, cancelNetwork := context.WithCancel(ctx)
		idle := &downloadIdleGuard{parent: ctx, cancel: cancelNetwork, timeout: a.effectiveStallTimeout()}
		defer idle.close()
		idle.begin()
		resp, e := a.client.Do(req.WithContext(networkCtx))
		e = idle.end(e)
		if e != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return classifyNetworkError(e)
		}
		defer resp.Body.Close()
		if offset == 0 && resp.StatusCode != http.StatusOK {
			return responseDownloadError(resp)
		}
		if offset > 0 {
			if resp.StatusCode != http.StatusPartialContent {
				if retryableHTTPStatus(resp.StatusCode) {
					return responseDownloadError(resp)
				}
				return errors.New("Publisher does not support resuming this partial; it was retained")
			}
			var first, last, total int64
			var extra string
			n, _ := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d%s", &first, &last, &total, &extra)
			if n != 3 || first != offset || last < first || last >= r.Bytes || total != r.Bytes {
				return errors.New("Publisher returned an unexpected range; partial retained")
			}
		}
		done := offset
		sinceSync := int64(0)
		meter := newTransferMeter(time.Now(), offset)
		a.setJob(job{State: "downloading", Name: r.Name, Done: done, Total: r.Bytes, transfer: meter})
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			readBuffer := buf
			if limiter.limited() {
				readBuffer = buf[:bandwidthChunk]
			}
			n, e := idle.read(resp.Body, readBuffer)
			if n > 0 {
				if done+int64(n) > r.Bytes {
					return errors.New("Download exceeds the expected size; partial retained")
				}
				if err = limiter.wait(ctx, n); err != nil {
					return err
				}
				if err = checkDirectory(); err != nil {
					return err
				}
				if err = a.writeDownloadChunk(ctx, f, buf[:n]); err != nil {
					return err
				}
				h.Write(buf[:n])
				done += int64(n)
				sinceSync += int64(n)
				if sinceSync >= 64*1024*1024 {
					if err = f.Sync(); err != nil {
						return err
					}
					discardDownloadCache(f, done-sinceSync, sinceSync)
					sinceSync = 0
				}
				meter.record(time.Now(), done)
				a.setJob(job{State: "downloading", Name: r.Name, Done: done, Total: r.Bytes, transfer: meter})
			}
			if e == io.EOF {
				break
			}
			if e != nil {
				return classifyNetworkError(e)
			}
		}
		if done != r.Bytes {
			return &transientDownloadError{err: errors.New("Download ended early; partial retained for resume")}
		}
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), r.SHA256) {
		return errors.New("Size or SHA-256 mismatch; partial retained for inspection")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	discardDownloadCache(f, 0, 0)
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h.Reset()
	checked = 0
	dropped = 0
	a.setJob(job{State: "verifying", Name: r.Name, Total: r.Bytes})
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, e := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			checked += int64(n)
			if checked-dropped >= 64*1024*1024 {
				discardDownloadCache(f, dropped, checked-dropped)
				dropped = checked
			}
			a.setJob(job{State: "verifying", Name: r.Name, Done: checked, Total: r.Bytes})
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	if checked != r.Bytes || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), r.SHA256) {
		return errors.New("Downloaded file failed readback verification; partial retained")
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkDirectory(); err != nil {
		return err
	}
	// Install without replacing an existing destination, including a race.
	if err = installExclusive(part, final); err != nil {
		return fmt.Errorf("Cannot install verified download: %w", err)
	}
	if err = os.Remove(metadata); err != nil {
		log.Printf("Installed %s, but could not remove resume information: %v", r.Name, err)
	}
	return nil
}

const gibibyte uint64 = 1024 * 1024 * 1024
const freeSpaceReserve uint64 = 2 * gibibyte
const maxReserveGiB = 1_000_000

// Call while holding a.mu, or before the app becomes available to other goroutines.
func (a *app) effectiveReserveGiB() int {
	if a.reserveGiB == 0 {
		return 2
	}
	return a.reserveGiB
}

func spaceSufficient(free, remaining, reserve uint64) bool {
	return remaining == 0 || (free >= reserve && remaining <= free-reserve)
}
func replaceResumeInfo(dir, metadata string, info resumeRecord) error {
	f, err := os.CreateTemp(dir, ".resume-metadata-*.part")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = json.NewEncoder(f).Encode(info); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, metadata)
}
func min64(a, b int64) int {
	if a < b {
		return int(a)
	}
	return int(b)
}
func activeDownload(state string) bool {
	return state == "waiting" || state == "retrying" || state == "resolving" || state == "checking" || state == "downloading" || state == "verifying" || state == "discarding" || state == "repairing" || state == "indexing"
}
func (a *app) beginDownload(initial job, resolve func(context.Context) (downloadRequest, error)) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.beginDownloadLocked(initial, resolve)
}

// beginDownloadLocked starts exactly one job; callers hold a.mu.
func (a *app) beginDownloadLocked(initial job, resolve func(context.Context) (downloadRequest, error)) bool {
	if a.closing || a.queueActive || activeDownload(a.current.State) {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelDownload = cancel
	a.lastDownload = nil
	a.retryResolve = resolve
	a.retryInitial = initial
	a.current = initial
	a.retryCount = 0
	a.lastHTTPStatus = 0
	go func() {
		defer cancel()
		var item downloadRequest
		err := a.withNetworkRetries(ctx, initial.Name, func() error { var e error; item, e = resolve(ctx); return e })
		if err == nil {
			a.mu.Lock()
			copy := item
			a.lastDownload = &copy
			a.mu.Unlock()
			err = a.obtainCollection(ctx, item)
		}
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.cancelDownload = nil
		if !errors.Is(err, context.Canceled) {
			title := item.Name
			if title == "" {
				title = initial.Name
			}
			oldHistory := a.downloadHistory
			a.appendHistoryLocked(item.CatalogID, title, err, a.retryCount, a.lastHTTPStatus)
			if a.queueStore != nil {
				if saveErr := a.saveQueueLocked(nil, a.catalogQueue); saveErr != nil {
					a.downloadHistory = oldHistory
					a.current = job{State: "failed", Name: item.Name, Error: "Download finished, but history could not be saved: " + saveErr.Error()}
					return
				}
			}
		}
		if err == nil {
			a.current = job{State: "complete", Name: item.Name, Done: item.Bytes, Total: item.Bytes}
		} else if errors.Is(err, context.Canceled) {
			message := "Download canceled. Any matching partial was retained."
			if a.current.State == "repairing" {
				message = "Paused. Link repair will restart when you continue."
			}
			if a.current.State == "indexing" {
				message = "Paused. Search preparation will restart when you continue."
			}
			a.current = job{State: "canceled", Name: item.Name, Error: message}
		} else {
			a.current = job{State: "failed", Name: item.Name, Error: err.Error()}
		}
	}()
	return true
}

// Read the selected directory directly: globbing the whole path treats user
// folder names containing brackets or wildcard characters as patterns.
func libraryArchives(root string) ([]string, error) {
	dir := filepath.Join(root, "content", "zim")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // A new library need not have a collection folder yet.
	}
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".zim") {
			if !completedRepair(dir, entry.Name()) {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
	}
	return files, nil
}

func (a *app) startReader() (string, error) {
	if err := a.checkLibraryRoot(); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.readerCmd != nil {
		if a.homeURL != "" {
			return a.homeURL, nil
		}
		return a.readerURL, nil
	}
	files, err := libraryArchives(a.root)
	if err != nil {
		return "", fmt.Errorf("Cannot read collection folder: %w", err)
	}
	if len(files) == 0 {
		return "", errors.New("No ZIM archives found in content/zim")
	}
	if a.reader == "" {
		return "", errors.New("Kiwix reader is missing. Place kiwix-serve next to the application or supply --reader.")
	}
	var homeListener net.Listener
	var homeServer *http.Server
	port := 0
	a.homeWarning = ""
	if info, e := os.Stat(filepath.Join(a.root, "index.html")); e == nil && info.Mode().IsRegular() {
		var e error
		homeListener, e = net.Listen("tcp", "127.0.0.1:8765")
		if e == nil {
			var probe net.Listener
			probe, e = net.Listen("tcp", "127.0.0.1:8766")
			if e == nil {
				_ = probe.Close()
				var handler http.Handler
				handler, e = homepageHandler(a.root)
				if e == nil {
					homeServer = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
					port = 8766
				}
			}
			if e != nil {
				_ = homeListener.Close()
				homeListener = nil
			}
		}
		if e != nil {
			a.homeWarning = "The custom homepage needs local ports 8765 and 8766; opening the Kiwix catalog instead."
		}
	}
	if port == 0 {
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			return "", e
		}
		port = listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
	}
	args := []string{"--address=127.0.0.1", "--port=" + strconv.Itoa(port), "--blockexternal", "--threads=2"}
	if catalogMatches(filepath.Join(a.root, "library.xml"), a.root, files) {
		args = append(args, "--library", filepath.Join(a.root, "library.xml"))
	} else {
		args = append(args, files...)
	}
	cmd := exec.Command(a.reader, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		if homeListener != nil {
			_ = homeListener.Close()
		}
		return "", err
	}
	a.readerCmd = cmd
	a.readerURL = fmt.Sprintf("http://127.0.0.1:%d/", port)
	if homeServer != nil {
		a.homeURL = "http://127.0.0.1:8765/"
		go homeServer.Serve(homeListener)
	}
	go func() {
		cmd.Wait()
		a.mu.Lock()
		if a.readerCmd == cmd {
			a.readerCmd = nil
			a.readerURL = ""
			a.homeURL = ""
		}
		a.mu.Unlock()
		if homeServer != nil {
			_ = homeServer.Close()
		}
	}()
	if a.homeURL != "" {
		return a.homeURL, nil
	}
	return a.readerURL, nil
}

// Only use a saved catalog when it refers to exactly the selected local archives.
func catalogMatches(path, root string, files []string) bool {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 16*1024*1024 {
		return false
	}
	var catalog struct {
		Books []struct {
			Path string `xml:"path,attr"`
		} `xml:"book"`
	}
	if xml.Unmarshal(b, &catalog) != nil || len(catalog.Books) != len(files) {
		return false
	}
	expected := make(map[string]bool, len(files))
	for _, f := range files {
		expected[filepath.Clean(f)] = true
	}
	for _, book := range catalog.Books {
		if filepath.IsAbs(book.Path) {
			return false
		}
		f := filepath.Clean(filepath.Join(root, filepath.FromSlash(book.Path)))
		if !expected[f] {
			return false
		}
		delete(expected, f)
	}
	return len(expected) == 0
}
func (a *app) stopReader() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.readerCmd != nil {
		_ = a.readerCmd.Process.Kill()
	}
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	a.addQueueHandlers(mux)
	a.addBandwidthHandler(mux)
	mux.HandleFunc("/api/library/hybrid", a.hybridLibrarySearch)
	a.addStageCopyHandler(mux)
	a.addBenchmarkHandler(mux)
	a.addDriveHandler(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := assets.ReadFile("index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(strings.ReplaceAll(string(b), "/*SEARCH_SPACE_PROFILES*/null", string(searchSpaceJSON))))
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		now := time.Now()
		j, u := a.current.withTransferProgress(now), a.readerURL
		retryAvailable := a.retryResolve != nil
		queued := append([]queuedCollection{}, a.catalogQueue...)
		var queueCurrent *queuedCollection
		if a.queueCurrent != nil {
			item := *a.queueCurrent
			queueCurrent = &item
		}
		queueActive := a.queueActive
		concurrency := a.effectiveDownloadConcurrency()
		stallSeconds := a.effectiveStallSeconds()
		bandwidthKiB := a.bandwidthKiB
		history := append([]downloadHistoryEntry{}, a.downloadHistory...)
		budget := a.activeBudget
		running := make([]map[string]any, 0, len(a.queueInFlight))
		for _, item := range a.queueInFlight {
			running = append(running, map[string]any{"id": item.ID, "title": item.Title, "approxBytes": item.ApproxBytes, "paused": item.Paused, "job": a.queueJobs[item.ID].withTransferProgress(now)})
		}
		queueError := a.queueStoreError
		reserveGiB := a.effectiveReserveGiB()
		var last *downloadRequest
		if a.lastDownload != nil {
			copy := *a.lastDownload
			last = &copy
		}
		a.mu.Unlock()
		rootErr := a.checkLibraryRoot()
		var files []string
		if rootErr == nil {
			var err error
			files, err = libraryArchives(a.root)
			if err != nil {
				rootErr = fmt.Errorf("Cannot read collection folder: %w", err)
			}
		}
		state := map[string]any{"job": j, "readerURL": u, "archiveCount": len(files), "root": a.root, "queue": queued, "queueCurrent": queueCurrent, "queueActive": queueActive, "queueRunning": running, "downloadConcurrency": concurrency, "downloadStallSeconds": stallSeconds, "bandwidthKiB": bandwidthKiB, "downloadHistory": history, "queueError": queueError, "reserveBytes": uint64(reserveGiB) * gibibyte}
		if rootErr != nil {
			state["libraryError"] = rootErr.Error()
			state["readerURL"] = ""
		}
		canRetry := rootErr == nil && retryAvailable && (j.State == "failed" || j.State == "canceled")
		if rootErr == nil {
			partials := listSavedPartials(a.root)
			for i := range partials {
				for _, item := range queued {
					if partials[i].CatalogID == item.ID {
						partials[i].Queued = true
						break
					}
				}
				for _, item := range running {
					if partials[i].CatalogID == item["id"] {
						partials[i].Queued = true
					}
				}
			}
			state["partials"] = partials
			report := queueSpaceSnapshot(budget, queued, running, partials, j, last)
			if budget == nil && last != nil && activeDownload(j.State) && !strings.HasPrefix(j.Name, "Copying ") {
				report.ReservedPreparationBytes, _ = a.collectionExtraSpace(*last)
			}
			state["queueSpace"] = report
		}
		state["canRetry"] = canRetry
		state["canDiscard"] = canRetry && last != nil && matchingPartial(a.root, *last)
		if rootErr == nil {
			spacePath, err := downloadSpacePath(a.root)
			if err == nil {
				var free uint64
				free, err = availableSpace(spacePath)
				if err == nil {
					state["freeBytes"] = free
				}
			}
			if err != nil {
				state["spaceError"] = "Unable to check free space for collections"
			}
		}
		writeJSON(w, state)
	})
	mux.HandleFunc("/api/library/collections", func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkLibraryRoot(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		items, err := a.localCollections(ctx)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		writeJSON(w, map[string]any{"collections": items})
	})
	mux.HandleFunc("/api/library/search", func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkLibraryRoot(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		a.librarySearch(w, r)
	})
	mux.HandleFunc("/api/catalog", func(w http.ResponseWriter, r *http.Request) {
		start, err := strconv.Atoi(r.URL.Query().Get("start"))
		if err != nil && r.URL.Query().Get("start") != "" {
			http.Error(w, "Invalid page", 400)
			return
		}
		if start < 0 || start > 1000000 {
			http.Error(w, "Invalid page", 400)
			return
		}
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(query) > 200 {
			http.Error(w, "Search is too long", 400)
			return
		}
		language := r.URL.Query().Get("lang")
		if language != "" && (len(language) != 3 || strings.Trim(language, "abcdefghijklmnopqrstuvwxyz") != "") {
			http.Error(w, "Invalid language", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		items, total, err := searchCatalog(ctx, a.client, kiwixCatalog, query, language, start)
		if err != nil {
			http.Error(w, "Could not load the Kiwix catalog: "+err.Error(), 502)
			return
		}
		writeJSON(w, map[string]any{"items": items, "total": total, "start": start})
	})
	mux.HandleFunc("/api/catalog/download", func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkLibraryRoot(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		var request struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request); err != nil || !uuidPattern.MatchString(request.ID) {
			http.Error(w, "Invalid collection ID", 400)
			return
		}
		started := a.beginDownload(job{State: "resolving"}, func(ctx context.Context) (downloadRequest, error) {
			metadataCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			item, err := resolveCollection(metadataCtx, a.client, kiwixCatalog, request.ID)
			item.CatalogID = request.ID
			return item, err
		})
		if !started {
			http.Error(w, "A download is already running", 409)
			return
		}
		writeJSON(w, map[string]bool{"started": true})
	})
	mux.HandleFunc("/api/download/resume-saved", func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkLibraryRoot(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		var request struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request); err != nil {
			http.Error(w, "Invalid saved download", 400)
			return
		}
		saved, ok := savedPartialByName(a.root, request.Name)
		if !ok || !uuidPattern.MatchString(saved.CatalogID) {
			http.Error(w, "Saved catalog download not found", 404)
			return
		}
		started := a.beginDownload(job{State: "resolving", Name: saved.Name}, func(ctx context.Context) (downloadRequest, error) {
			metadataCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			item, err := resolveCollection(metadataCtx, a.client, kiwixCatalog, saved.CatalogID)
			if err != nil {
				return downloadRequest{}, err
			}
			item.CatalogID = saved.CatalogID
			if item.Name != saved.Name || !matchingPartial(a.root, item) {
				return downloadRequest{}, errors.New("Publisher details changed or the saved partial is unavailable; it was retained")
			}
			return item, nil
		})
		if !started {
			http.Error(w, "A download is already running", 409)
			return
		}
		writeJSON(w, map[string]bool{"started": true})
	})
	mux.HandleFunc("/api/download/discard-saved", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Name        string `json:"name"`
			Fingerprint string `json:"fingerprint"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request); err != nil {
			http.Error(w, "Invalid saved download", 400)
			return
		}
		a.mu.Lock()
		if a.closing || a.queueActive || activeDownload(a.current.State) {
			a.mu.Unlock()
			http.Error(w, "Wait for the current download to finish", 409)
			return
		}
		previous := a.current
		a.current = job{State: "discarding", Name: request.Name}
		a.mu.Unlock()
		err := a.checkLibraryRoot()
		if err == nil {
			err = discardSavedPartial(a.root, request.Name, request.Fingerprint)
		}
		a.mu.Lock()
		a.current = previous
		if err == nil && a.lastDownload != nil && a.lastDownload.Name == request.Name {
			a.current = job{State: "idle"}
			a.lastDownload = nil
			a.retryResolve = nil
		}
		a.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeJSON(w, map[string]bool{"discarded": true})
	})
	mux.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkLibraryRoot(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		var x downloadRequest
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&x); e != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		if e := validRequest(x); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		started := a.beginDownload(job{State: "checking", Name: x.Name, Total: x.Bytes}, func(context.Context) (downloadRequest, error) { return x, nil })
		if !started {
			http.Error(w, "A download is already running", 409)
			return
		}
		writeJSON(w, map[string]bool{"started": true})
	})
	mux.HandleFunc("/api/download/retry", func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkLibraryRoot(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		a.mu.Lock()
		if a.retryResolve == nil || (a.current.State != "failed" && a.current.State != "canceled") {
			a.mu.Unlock()
			http.Error(w, "No previous download to retry", 409)
			return
		}
		started := a.beginDownloadLocked(a.retryInitial, a.retryResolve)
		a.mu.Unlock()
		if !started {
			http.Error(w, "A download is already running", 409)
			return
		}
		writeJSON(w, map[string]bool{"started": true})
	})
	mux.HandleFunc("/api/download/discard", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		if a.closing || a.queueActive || activeDownload(a.current.State) || a.lastDownload == nil || (a.current.State != "failed" && a.current.State != "canceled") {
			a.mu.Unlock()
			http.Error(w, "No safely identifiable partial to discard", 409)
			return
		}
		saved := *a.lastDownload
		previous := a.current
		a.current = job{State: "discarding", Name: saved.Name}
		a.mu.Unlock()
		err := a.checkLibraryRoot()
		if err == nil {
			err = discardPartial(a.root, saved)
		}
		a.mu.Lock()
		if err == nil {
			a.current = job{State: "idle"}
			a.lastDownload = nil
			a.retryResolve = nil
		} else {
			a.current = previous
		}
		a.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeJSON(w, map[string]bool{"discarded": true})
	})
	mux.HandleFunc("/api/download/cancel", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		cancel := a.cancelDownload
		a.mu.Unlock()
		if cancel == nil {
			http.Error(w, "No download is running", 409)
			return
		}
		cancel()
		writeJSON(w, map[string]bool{"canceling": true})
	})
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		u, e := a.startReader()
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		a.mu.Lock()
		readerURL, warning := a.readerURL, a.homeWarning
		a.mu.Unlock()
		writeJSON(w, map[string]string{"url": u, "readerURL": readerURL, "warning": warning})
	})
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		a.stopReader()
		writeJSON(w, map[string]bool{"stopped": true})
	})
	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		busy := a.queueActive || activeDownload(a.current.State)
		if !busy {
			a.closing = true
		}
		a.mu.Unlock()
		if busy {
			http.Error(w, "Wait for the current download to finish", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"stopping": true})
		if a.shutdown != nil {
			go a.shutdown()
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "127.0.0.1:") {
			http.Error(w, "Local access only", 403)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Header.Get("X-Library-Token") != a.token {
				http.Error(w, "Forbidden", 403)
				return
			}
			if r.URL.Path != "/api/state" && r.URL.Path != "/api/catalog" && r.URL.Path != "/api/drives" && !strings.HasPrefix(r.URL.Path, "/api/library/") && r.Method != "POST" {
				http.Error(w, "Method not allowed", 405)
				return
			}
			if (r.URL.Path == "/api/state" || r.URL.Path == "/api/catalog" || r.URL.Path == "/api/drives" || strings.HasPrefix(r.URL.Path, "/api/library/")) && r.Method != "GET" {
				http.Error(w, "Method not allowed", 405)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func openBrowser(u string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		c = exec.Command("open", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	return c.Start()
}
func main() {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(768 << 20)
	}
	if os.Getenv("GOMAXPROCS") == "" && runtime.GOMAXPROCS(0) > 2 {
		runtime.GOMAXPROCS(2)
	}
	root := flag.String("library", "", "library directory (default: application directory)")
	reader := flag.String("reader", "", "path to bundled kiwix-serve")
	searchPath := flag.String("search-indexes", "", "prebuilt search index folder (default: bundled assets)")
	noBrowser := flag.Bool("no-browser", false, "do not open the control panel automatically")
	flag.Parse()
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	if *root == "" {
		*root = filepath.Dir(exe)
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		log.Fatal(err)
	}
	if *reader == "" {
		name := "kiwix-serve"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		p := filepath.Join(filepath.Dir(exe), name)
		if s, e := os.Stat(p); e == nil && !s.IsDir() {
			*reader = p
		}
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		log.Fatal(err)
	}
	rootIdentity, err := directoryIdentity(abs)
	if err != nil {
		log.Fatal("Library folder is missing or is not a directory; create or choose it first")
	}
	a := &app{searchDir: filepath.Join(filepath.Dir(exe), "..", "search"), root: abs, rootIdentity: rootIdentity, reader: *reader, token: hex.EncodeToString(b), current: job{State: "idle"}, client: &http.Client{Timeout: 24 * time.Hour, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "https" {
			return errors.New("Unsafe download redirect")
		}
		return nil
	}}}
	if *searchPath != "" {
		a.searchDir = *searchPath
	}
	if _, err := os.Stat(a.searchDir); err != nil {
		a.searchDir = ""
	}
	if err := a.initializeQueueStore(); err != nil {
		a.queueStoreError = "Download list unavailable: " + err.Error() + ". Check the library and restart the app."
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second}
	a.shutdown = func() { a.stopReader(); _ = srv.Shutdown(context.Background()) }
	u := "http://" + ln.Addr().String() + "/#" + a.token
	fmt.Println("Offline Library control panel:", u)
	if !*noBrowser {
		if err = openBrowser(u); err != nil {
			fmt.Fprintln(os.Stderr, "Open the address above in a browser:", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() { <-ctx.Done(); a.stopReader(); srv.Shutdown(context.Background()) }()
	if err = srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
