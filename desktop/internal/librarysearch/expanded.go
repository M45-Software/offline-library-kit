package librarysearch

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type expandConfig struct {
	ForceCustomSearch bool         `json:"force_custom_search,omitempty"`
	Input             string       `json:"input"`
	Sources           string       `json:"sources"`
	Metadata          string       `json:"metadata"`
	Catalog           string       `json:"catalog"`
	Reader            string       `json:"reader"`
	Work              string       `json:"work"`
	VolumeUUID        string       `json:"volume_uuid"`
	ReserveGiB        uint64       `json:"reserve_gib"`
	Workers           int          `json:"workers"`
	BatchPages        int          `json:"batch_pages"`
	checkVolume       func() error // Injected only by tests; configuration always uses the real mount guard.
}
type sourceQueue struct {
	Name, Label, Source                  string
	SourceBytes, SourceMtime, QueueBytes int64
	Records                              int64
}
type expandPlan struct {
	Sampling    *sampleProvenance `json:",omitempty"`
	Version     int
	Created     string
	Config      expandConfig
	Queues      []sourceQueue
	InputSHA256 string
}
type expandState struct {
	Batch                                            int
	Offset                                           int64
	Attempted, Indexed, Passages, Skipped, Truncated int64
	Done                                             bool
}
type storedShard struct {
	Name   string
	Bytes  int64
	SHA256 string
}
type batchCommit struct {
	ElapsedSeconds float64
	StoredBytes    int64
	After          expandState
	Shards         []storedShard
	Skipped        []map[string]string `json:",omitempty"`
}

func atomicJSON(path string, v any) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".save-*.tmp")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if e = enc.Encode(v); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}
func volumeGuard(root, uuid string, reserve uint64) error {
	if uuid == "" {
		return errors.New("volume_uuid is required")
	}
	existing := root
	for {
		if _, e := os.Stat(existing); e == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return errors.New("cannot locate work volume")
		}
		existing = parent
	}
	b, e := exec.Command("findmnt", "-J", "-T", existing, "-o", "UUID,TARGET,OPTIONS").Output()
	if e != nil {
		return e
	}
	var m struct {
		Filesystems []struct{ UUID, Target, Options string }
	}
	if e = json.Unmarshal(b, &m); e != nil || len(m.Filesystems) != 1 {
		return errors.New("cannot inspect work volume")
	}
	v := m.Filesystems[0]
	rel, e := filepath.Rel(v.Target, root)
	if e != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || v.UUID != uuid || !strings.Contains(","+v.Options+",", ",rw,") {
		return errors.New("work volume UUID, path or write access changed")
	}
	free, e := availableBytes(existing)
	if e != nil {
		return e
	}
	if free < reserve {
		return fmt.Errorf("work volume free-space reserve reached: %d bytes free", free)
	}
	return nil
}
func (c expandConfig) guard() error {
	if c.checkVolume != nil {
		return c.checkVolume()
	}
	return volumeGuard(c.Work, c.VolumeUUID, c.ReserveGiB<<30)
}
func normalizeConfig(c expandConfig) (expandConfig, error) {
	if c.Work == "" || c.Input == "" || c.Sources == "" || c.Catalog == "" || c.Metadata == "" || c.Reader == "" {
		return c, errors.New("input, sources, catalog, metadata, reader and work are required")
	}
	var e error
	c.Work, e = filepath.Abs(c.Work)
	if e != nil {
		return c, e
	}
	if c.Workers == 0 {
		c.Workers = runtime.GOMAXPROCS(0)
	}
	if c.Workers < 1 || c.Workers > 256 {
		return c, errors.New("workers must be 0 (automatic) or 1-256")
	}
	if c.BatchPages == 0 {
		c.BatchPages = c.Workers * 4
		if c.BatchPages < 32 {
			c.BatchPages = 32
		}
		if c.BatchPages > 256 {
			c.BatchPages = 256
		}
	}
	if c.BatchPages < 1 || c.BatchPages > 256 {
		return c, errors.New("batch_pages must be 1-256")
	}
	if c.ReserveGiB == 0 {
		c.ReserveGiB = 50
	}
	if c.ReserveGiB > 1<<20 {
		return c, errors.New("reserve too large")
	}
	return c, c.guard()
}
func readingCandidate(d doc, meta *metadata) bool {
	if strings.HasPrefix(d.Path, "offline-quality/") {
		return false
	}
	if !strings.HasPrefix(d.Collection, "gutenberg_") {
		return true
	}
	d = enrich(d, meta)
	return strings.Contains(d.MetadataSource, ":full_by_popularity.js#") && !strings.Contains(d.Path, "_cover.")
}
func prepareExpanded(c expandConfig) (expandPlan, error) {
	p := expandPlan{Version: 1, Created: time.Now().UTC().Format(time.RFC3339), Config: c}
	meta, e := loadMetadata(c.Metadata)
	if e != nil {
		return p, e
	}
	files, e := filepath.Glob(filepath.Join(c.Sources, "*.zim"))
	if e != nil || len(files) == 0 {
		return p, errors.New("no selected archives")
	}
	sort.Strings(files)
	for _, file := range files {
		s, e := os.Stat(file)
		if e != nil || !s.Mode().IsRegular() {
			return p, fmt.Errorf("source missing: %s", file)
		}
		name := strings.TrimSuffix(filepath.Base(file), ".zim")
		label := meta.Collections[name].Label
		if label == "" {
			label = name
		}
		p.Queues = append(p.Queues, sourceQueue{Name: name, Label: label, Source: file, SourceBytes: s.Size(), SourceMtime: s.ModTime().UnixNano()})
	}
	if e = os.Mkdir(c.Work, 0700); e != nil {
		return p, e
	}
	if e = os.Mkdir(filepath.Join(c.Work, "queues"), 0700); e != nil {
		return p, e
	}
	if e = os.Mkdir(filepath.Join(c.Work, "collections"), 0700); e != nil {
		return p, e
	}
	type writer struct {
		file  *os.File
		buf   *bufio.Writer
		queue int
	}
	writers := map[string]*writer{}
	defer func() {
		for _, w := range writers {
			w.file.Close()
		}
	}()
	for i, q := range p.Queues {
		f, e := os.OpenFile(filepath.Join(c.Work, "queues", q.Name+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return p, e
		}
		writers[q.Name] = &writer{f, bufio.NewWriterSize(f, 65536), i}
	}
	input, e := os.Open(c.Input)
	if e != nil {
		return p, e
	}
	defer input.Close()
	h := sha256.New()
	scan := bufio.NewScanner(io.TeeReader(input, h))
	scan.Buffer(make([]byte, 65536), 4<<20)
	var scanned int64
	for scan.Scan() {
		var d doc
		if e = json.Unmarshal(scan.Bytes(), &d); e != nil {
			return p, e
		}
		w, ok := writers[d.Collection]
		if !ok || !readingCandidate(d, meta) {
			continue
		}
		if _, e = w.buf.Write(scan.Bytes()); e != nil {
			return p, e
		}
		if e = w.buf.WriteByte('\n'); e != nil {
			return p, e
		}
		p.Queues[w.queue].Records++
		scanned++
		if scanned%1000000 == 0 {
			fmt.Fprintf(os.Stderr, "Prepared %d selected page records\n", scanned)
			if e = c.guard(); e != nil {
				return p, e
			}
		}
	}
	if e = scan.Err(); e != nil {
		return p, e
	}
	for _, w := range writers {
		if e = w.buf.Flush(); e != nil {
			return p, e
		}
		if e = w.file.Sync(); e != nil {
			return p, e
		}
		st, _ := w.file.Stat()
		p.Queues[w.queue].QueueBytes = st.Size()
	}
	p.InputSHA256 = hex.EncodeToString(h.Sum(nil))
	if e = writeExpandedCatalog(c, p.Queues); e != nil {
		return p, e
	}
	if e = atomicJSON(filepath.Join(c.Work, "plan.json"), p); e != nil {
		return p, e
	}
	fmt.Fprintf(os.Stderr, "Prepared %d collections, %d page records\n", len(p.Queues), scanned)
	return p, nil
}
func writeExpandedCatalog(c expandConfig, qs []sourceQueue) error {
	f, e := os.Open(c.Catalog)
	if e != nil {
		return e
	}
	defer f.Close()
	type book struct {
		Attrs []xml.Attr `xml:",any,attr"`
		Inner string     `xml:",innerxml"`
	}
	var original struct {
		Books []book `xml:"book"`
	}
	if e = xml.NewDecoder(f).Decode(&original); e != nil {
		return e
	}
	selected := map[string]sourceQueue{}
	for _, q := range qs {
		selected[q.Name+".zim"] = q
	}
	var result struct {
		XMLName xml.Name `xml:"library"`
		Version string   `xml:"version,attr"`
		Books   []book   `xml:"book"`
	}
	result.Version = "20110515"
	for _, b := range original.Books {
		for i, a := range b.Attrs {
			if a.Name.Local != "path" {
				continue
			}
			q, ok := selected[filepath.Base(a.Value)]
			if ok {
				source, e := filepath.Abs(q.Source)
				if e != nil {
					return e
				}
				b.Attrs[i].Value = source
				result.Books = append(result.Books, b)
				delete(selected, filepath.Base(a.Value))
			}
			break
		}
	}
	if len(selected) > 0 {
		return errors.New("catalog is missing selected archives")
	}
	b, e := xml.MarshalIndent(result, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(c.Work, "reader-library.xml"), b, 0600)
}
func startExpandedReader(ctx context.Context, p expandPlan) (string, func(), error) {
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return "", nil, e
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	logfile, e := os.OpenFile(filepath.Join(p.Config.Work, "reader.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return "", nil, e
	}
	cmd := exec.CommandContext(ctx, p.Config.Reader, "--address=127.0.0.1", fmt.Sprintf("--port=%d", port), fmt.Sprintf("--threads=%d", p.Config.Workers), "--blockexternal", "--library", filepath.Join(p.Config.Work, "reader-library.xml"))
	cmd.Stdout = logfile
	cmd.Stderr = logfile
	if e = cmd.Start(); e != nil {
		logfile.Close()
		return "", nil, e
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stop := func() { cmd.Process.Kill(); <-done; logfile.Close() }
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := http.Client{Timeout: time.Second}
	for i := 0; i < 120; i++ {
		select {
		case err := <-done:
			logfile.Close()
			return "", nil, fmt.Errorf("reader exited: %v", err)
		default:
		}
		r, e := client.Get(base + "/catalog/v2/entries?count=1")
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return base, stop, nil
			}
		}
		select {
		case <-ctx.Done():
			stop()
			return "", nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	stop()
	return "", nil, errors.New("reader startup timed out")
}
func loadExpandState(dir string) (expandState, error) {
	var state expandState
	items, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return state, nil
	}
	if e != nil {
		return state, e
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
	for _, item := range items {
		if !item.IsDir() || strings.Contains(item.Name(), ".") {
			continue
		}
		if item.Name() != fmt.Sprintf("batch-%06d", state.Batch+1) {
			return state, errors.New("batch sequence is not contiguous")
		}
		b, e := os.ReadFile(filepath.Join(dir, item.Name(), "commit.json"))
		if e != nil {
			return state, e
		}
		var commit batchCommit
		if e = json.Unmarshal(b, &commit); e != nil {
			return state, e
		}
		if commit.After.Batch != state.Batch+1 || commit.After.Offset < state.Offset {
			return state, errors.New("invalid batch cursor")
		}
		for _, shard := range commit.Shards {
			if filepath.Base(shard.Name) != shard.Name {
				return state, errors.New("invalid shard name")
			}
			st, e := os.Stat(filepath.Join(dir, item.Name(), shard.Name))
			if e != nil || st.Size() != shard.Bytes {
				return state, errors.New("completed shard missing or changed")
			}
		}
		state = commit.After
	}
	return state, nil
}

type queuedPage struct{ d doc }
type pageOutput struct {
	Index  int
	Docs   []doc
	Reason string
	Err    error
}

func fetchDeepPage(ctx context.Context, client *http.Client, base string, d doc, meta *metadata) pageOutput {
	d = enrich(d, meta)
	path := url.URL{Path: "/raw/" + d.Collection + "/content/" + d.Path}
	req, e := http.NewRequestWithContext(ctx, "GET", base+path.String(), nil)
	if e != nil {
		return pageOutput{Err: e}
	}
	resp, e := client.Do(req)
	if e != nil {
		return pageOutput{Err: fmt.Errorf("reader request failed: %w", e)}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return pageOutput{Reason: "archive entry unavailable"}
	}
	if resp.StatusCode != 200 {
		return pageOutput{Err: fmt.Errorf("reader returned HTTP %d", resp.StatusCode)}
	}
	kind := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(kind, "html") {
		return pageOutput{Reason: "entry is not HTML"}
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if e != nil {
		return pageOutput{Err: e}
	}
	if len(b) > 8<<20 {
		return pageOutput{Reason: "HTML exceeds 8 MiB extraction limit"}
	}
	chunks, e := passages(d, b, 200, 1000000)
	if e != nil {
		return pageOutput{Reason: "HTML could not be parsed: " + e.Error()}
	}
	if len(chunks) == 0 {
		return pageOutput{Reason: "no visible text in selected content region"}
	}
	return pageOutput{Docs: chunks}
}
func expandedBatch(ctx context.Context, p expandPlan, q sourceQueue, state expandState, readerURL string, meta *metadata, pageLimit int) (expandState, error) {
	started := time.Now()
	c := p.Config
	if pageLimit < 1 || pageLimit > 256 {
		return state, errors.New("page limit must be 1-256")
	}
	if e := c.guard(); e != nil {
		return state, e
	}
	st, e := os.Stat(q.Source)
	if e != nil || st.Size() != q.SourceBytes || st.ModTime().UnixNano() != q.SourceMtime {
		return state, errors.New("source archive changed or disappeared: " + q.Name)
	}
	queue := filepath.Join(c.Work, "queues", q.Name+".jsonl")
	st, e = os.Stat(queue)
	if e != nil || st.Size() != q.QueueBytes {
		return state, errors.New("page queue changed")
	}
	f, e := os.Open(queue)
	if e != nil {
		return state, e
	}
	defer f.Close()
	if _, e = f.Seek(state.Offset, 0); e != nil {
		return state, e
	}
	r := bufio.NewReaderSize(f, 65536)
	pages := make([]queuedPage, 0, pageLimit)
	after := state
	done := false
	for len(pages) < pageLimit {
		line, e := r.ReadBytes('\n')
		if len(line) > 4<<20 {
			return state, errors.New("queue record too large")
		}
		if len(line) > 0 {
			var d doc
			if err := json.Unmarshal(line, &d); err != nil {
				return state, err
			}
			if d.Collection != q.Name {
				return state, errors.New("queue collection mismatch")
			}
			if d.Path == "" {
				return state, errors.New("invalid entry path")
			}
			for _, part := range strings.Split(d.Path, "/") {
				if part == ".." || part == "." {
					return state, errors.New("invalid entry path")
				}
			}
			pages = append(pages, queuedPage{d})
			after.Offset += int64(len(line))
		}
		if e == io.EOF {
			done = true
			break
		}
		if e != nil {
			return state, e
		}
	}
	after.Batch++
	after.Done = done || after.Offset == q.QueueBytes
	dir := filepath.Join(c.Work, "collections", q.Name)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return state, e
	}
	name := fmt.Sprintf("batch-%06d", after.Batch)
	temp := filepath.Join(dir, name+".partial")
	if _, err := os.Stat(temp); err == nil {
		if e = os.Rename(temp, temp+fmt.Sprintf(".abandoned-%d", time.Now().UnixNano())); e != nil {
			return state, e
		}
	}
	if e = os.Mkdir(temp, 0700); e != nil {
		return state, e
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	transport := &http.Transport{MaxConnsPerHost: c.Workers, MaxIdleConnsPerHost: c.Workers, DisableCompression: true}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	jobs := make(chan int)
	outputs := make(chan pageOutput, c.Workers)
	window := make(chan struct{}, c.Workers)
	var wg sync.WaitGroup
	for w := 0; w < c.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				result := fetchDeepPage(ctx, client, readerURL, pages[i].d, meta)
				result.Index = i
				select {
				case outputs <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range pages {
			select {
			case window <- struct{}{}:
			case <-ctx.Done():
				return
			}
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(outputs) }()
	type shardJob struct {
		name string
		docs []doc
	}
	buildJobs := make(chan shardJob, 2)
	var builders sync.WaitGroup
	var buildMu sync.Mutex
	var buildErr error
	stored := map[string]storedShard{}
	buildWorkers := c.Workers
	if buildWorkers > 8 {
		buildWorkers = 8
	}
	for i := 0; i < buildWorkers; i++ {
		builders.Add(1)
		go func() {
			defer builders.Done()
			for job := range buildJobs {
				if ctx.Err() != nil {
					return
				}
				path := filepath.Join(temp, job.name)
				err := buildShardVersion(path, job.docs, 32768, "adaptive", 3)
				var hash string
				var size int64
				if err == nil {
					file, e := os.Open(path)
					if e != nil {
						err = e
					} else {
						h := sha256.New()
						size, err = io.Copy(h, file)
						file.Close()
						hash = hex.EncodeToString(h.Sum(nil))
					}
				}
				buildMu.Lock()
				if err != nil {
					if buildErr == nil {
						buildErr = err
					}
					cancel()
				} else {
					stored[job.name] = storedShard{job.name, size, hash}
				}
				buildMu.Unlock()
			}
		}()
	}
	var shardNames []string
	buffer := make([]doc, 0, 500)
	submit := func() error {
		if len(buffer) == 0 {
			return nil
		}
		name := fmt.Sprintf("shard-%05d.bin", len(shardNames))
		select {
		case buildJobs <- shardJob{name, buffer}:
			shardNames = append(shardNames, name)
			buffer = make([]doc, 0, 500)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	pending := map[int]pageOutput{}
	next := 0
	var firstErr error
	commit := batchCommit{}
	consume := func(result pageOutput) error {
		after.Attempted++
		if result.Err != nil {
			return result.Err
		}
		if result.Reason != "" {
			after.Skipped++
			commit.Skipped = append(commit.Skipped, map[string]string{"path": pages[result.Index].d.Path, "reason": result.Reason})
			return nil
		}
		after.Indexed++
		after.Passages += int64(len(result.Docs))
		for _, d := range result.Docs {
			buffer = append(buffer, d)
			if len(buffer) >= 500 {
				if e := submit(); e != nil {
					return e
				}
			}
		}
		return nil
	}
	for result := range outputs {
		if firstErr != nil {
			continue
		}
		pending[result.Index] = result
		for {
			value, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			if err := consume(value); err != nil {
				firstErr = err
				cancel()
				break
			}
			<-window
			next++
		}
	}
	if firstErr == nil {
		firstErr = submit()
	}
	close(buildJobs)
	builders.Wait()
	if firstErr != nil {
		return state, firstErr
	}
	if buildErr != nil {
		return state, buildErr
	}
	if next != len(pages) {
		return state, errors.New("batch did not process every queued page")
	}
	if e = c.guard(); e != nil {
		return state, e
	}
	for _, name := range shardNames {
		shard, ok := stored[name]
		if !ok {
			return state, errors.New("missing built shard")
		}
		commit.Shards = append(commit.Shards, shard)
		commit.StoredBytes += shard.Bytes
	}
	commit.ElapsedSeconds = time.Since(started).Seconds()
	commit.After = after
	m := manifest{Version: 3, Documents: int(after.Passages - state.Passages), Shards: shardNames, BlockBytes: 32768, Codec: "adaptive", Collections: map[string]string{q.Name: q.Label}}
	if e = atomicJSON(filepath.Join(temp, "manifest.json"), m); e != nil {
		return state, e
	}
	if e = atomicJSON(filepath.Join(temp, "commit.json"), commit); e != nil {
		return state, e
	}
	if e = os.Rename(temp, filepath.Join(dir, name)); e != nil {
		return state, e
	}
	fmt.Fprintf(os.Stderr, "Committed %s batch %d: %d/%d pages, %d passages, %d skipped, done=%t (%.2fs, %.2f MiB)\n", q.Name, after.Batch, after.Attempted, q.Records, after.Passages, after.Skipped, after.Done, commit.ElapsedSeconds, float64(commit.StoredBytes)/(1<<20))
	return after, nil
}
func runExpanded(ctx context.Context, configPath string, maxRounds int, forceCustom bool) error {
	b, e := os.ReadFile(configPath)
	if e != nil {
		return e
	}
	var c expandConfig
	if e = json.Unmarshal(b, &c); e != nil {
		return e
	}
	c.ForceCustomSearch = c.ForceCustomSearch || forceCustom
	c, e = normalizeConfig(c)
	if e != nil {
		return e
	}
	var p expandPlan
	b, e = os.ReadFile(filepath.Join(c.Work, "plan.json"))
	if os.IsNotExist(e) {
		p, e = prepareExpanded(c)
		if e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else {
		if e = json.Unmarshal(b, &p); e != nil {
			return e
		}
		if p.Version != 1 || p.Config.VolumeUUID != c.VolumeUUID || p.Config.Work != c.Work || p.Config.Sources != c.Sources || p.Config.Input != c.Input || p.Config.Metadata != c.Metadata {
			return errors.New("saved plan differs from config")
		}
		p.Config = c
	}
	lock, e := os.OpenFile(filepath.Join(c.Work, "work.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = lockWorkFile(lock); e != nil {
		return errors.New("another expanded build is already running")
	}
	meta, e := loadMetadata(c.Metadata)
	if e != nil {
		return e
	}
	states := map[string]expandState{}
	for _, q := range p.Queues {
		s, e := loadExpandState(filepath.Join(c.Work, "collections", q.Name))
		if e != nil {
			return e
		}
		states[q.Name] = s
	}
	routes, e := policyForExpanded(c.Work)
	if e != nil {
		return e
	}
	var reused []string
	for _, q := range p.Queues {
		r, ok := routes[q.Name]
		if !ok {
			return fmt.Errorf("search policy missing %s", q.Name)
		}
		if !c.ForceCustomSearch && r.Backend == "native" {
			reused = append(reused, q.Name)
		}
	}
	pending := false
	for _, q := range p.Queues {
		if !states[q.Name].Done && (c.ForceCustomSearch || routes[q.Name].Backend != "native") {
			pending = true
		}
	}
	if !pending {
		return atomicJSON(filepath.Join(c.Work, "status.json"), map[string]any{"state": "selected_queues_processed", "collections": states, "reused_native": reused, "updated_utc": time.Now().UTC().Format(time.RFC3339), "note": "Native full-text collections are reused; skipped entries remain explicit in batch reports."})
	}
	base, stop, e := startExpandedReader(ctx, p)
	if e != nil {
		return e
	}
	defer stop()
	for round := 0; ; round++ {
		remaining := 0
		for _, q := range p.Queues {
			if !c.ForceCustomSearch && routes[q.Name].Backend == "native" {
				continue
			}
			s := states[q.Name]
			if s.Done {
				continue
			}
			remaining++
			limit := c.BatchPages
			if s.Batch == 0 {
				limit = 2
			}
			s, e = expandedBatch(ctx, p, q, s, base, meta, limit)
			if e != nil {
				atomicJSON(filepath.Join(c.Work, "status.json"), map[string]any{"state": "stopped_with_error", "error": e.Error(), "collection": q.Name, "collections": states})
				return e
			}
			states[q.Name] = s
			if e = atomicJSON(filepath.Join(c.Work, "status.json"), map[string]any{"state": "running", "reused_native": reused, "workers": c.Workers, "collections": states, "updated_utc": time.Now().UTC().Format(time.RFC3339)}); e != nil {
				return e
			}
		}
		if remaining == 0 {
			return atomicJSON(filepath.Join(c.Work, "status.json"), map[string]any{"state": "selected_queues_processed", "collections": states, "reused_native": reused, "note": "Skipped/limited entries remain explicit in per-batch reports; this is not proof of complete source coverage."})
		}
		if maxRounds > 0 && round+1 >= maxRounds {
			return atomicJSON(filepath.Join(c.Work, "status.json"), map[string]any{"state": "round_limit_reached", "workers": c.Workers, "collections": states, "updated_utc": time.Now().UTC().Format(time.RFC3339)})
		}
	}
}
