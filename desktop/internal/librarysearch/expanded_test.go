package librarysearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExpandedBatchResumeAndFailure(t *testing.T) {
	root := t.TempDir()
	c := expandConfig{Work: root, Workers: 4, checkVolume: func() error { return nil }}
	if err := os.Mkdir(filepath.Join(root, "queues"), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "sample.zim")
	if err := os.WriteFile(source, []byte("unchanged archive"), 0600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(source)
	q := sourceQueue{Name: "sample", Label: "Sample", Source: source, SourceBytes: st.Size(), SourceMtime: st.ModTime().UnixNano(), Records: 4}
	var lines strings.Builder
	for i := 0; i < 4; i++ {
		b, _ := json.Marshal(doc{Collection: q.Name, Title: fmt.Sprintf("Page %d", i), Path: fmt.Sprintf("A/page%d", i)})
		lines.Write(b)
		lines.WriteByte('\n')
	}
	queue := filepath.Join(root, "queues", q.Name+".jsonl")
	if err := os.WriteFile(queue, []byte(lines.String()), 0600); err != nil {
		t.Fatal(err)
	}
	q.QueueBytes = int64(lines.Len())
	p := expandPlan{Config: c}
	var failed atomic.Bool
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if failed.Load() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/raw/sample/content/A/page1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><body><main><h1 id='repair'>Repairs</h1><p>generator servicing engine oil replacement %s</p></main></body></html>", r.URL.Path)
	}))
	defer srv.Close()
	s, err := expandedBatch(context.Background(), p, q, expandState{}, srv.URL, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if s.Attempted != 2 || s.Indexed != 1 || s.Skipped != 1 || s.Done {
		t.Fatalf("unexpected first state: %+v", s)
	}
	dir := filepath.Join(root, "collections", q.Name)
	loaded, err := loadExpandState(dir)
	if err != nil || loaded != s {
		t.Fatalf("resume cursor: %+v %v", loaded, err)
	}
	en, err := openIndex(filepath.Join(dir, "batch-000001"), 1)
	if err != nil {
		t.Fatal(err)
	}
	count, _, err := en.queryFiltered("generator", "text", "")
	en.close()
	if err != nil || count != 1 {
		t.Fatalf("searchable committed batch: %d %v", count, err)
	}
	failed.Store(true)
	returned, err := expandedBatch(context.Background(), p, q, s, srv.URL, nil, 2)
	if err == nil || returned != s {
		t.Fatalf("failed batch advanced cursor: %+v %v", returned, err)
	}
	loaded, err = loadExpandState(dir)
	if err != nil || loaded != s {
		t.Fatalf("partial batch changed state: %+v %v", loaded, err)
	}
	failed.Store(false)
	final, err := expandedBatch(context.Background(), p, q, s, srv.URL, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !final.Done || final.Attempted != 4 || final.Indexed != 3 || final.Skipped != 1 {
		t.Fatalf("unexpected final state: %+v", final)
	}
	loaded, err = loadExpandState(dir)
	if err != nil || loaded != final {
		t.Fatalf("final resume state: %+v %v", loaded, err)
	}
	if requests.Load() != 6 {
		t.Fatalf("expected each successful/failing batch request once, got %d", requests.Load())
	}
	if err = os.WriteFile(source, []byte("archive has changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = expandedBatch(context.Background(), p, q, final, srv.URL, nil, 2); err == nil {
		t.Fatal("changed source accepted")
	}
	file := filepath.Join(dir, "batch-000001", "shard-00000.bin")
	if err = os.WriteFile(file, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadExpandState(dir); err == nil {
		t.Fatal("changed shard accepted")
	}
}

func TestExpandedConfigRequiresExplicitWorkDirectory(t *testing.T) {
	_, err := normalizeConfig(expandConfig{Input: "i", Sources: "s", Catalog: "c", Metadata: "m", Reader: "r", checkVolume: func() error { return nil }})
	if err == nil {
		t.Fatal("empty work directory accepted")
	}
}

func TestDeepFetchPreservesSlashPrefixedEntry(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<main><p>A null device discards data.</p></main>")
	}))
	defer srv.Close()
	d := doc{Collection: "test", Path: "/dev/null", Title: "/dev/null"}
	if e := validateCompactDoc(d); e != nil {
		t.Fatal(e)
	}
	result := fetchDeepPage(context.Background(), srv.Client(), srv.URL, d, nil)
	if result.Err != nil || result.Reason != "" || len(result.Docs) != 1 || got != "/raw/test/content//dev/null" {
		t.Fatalf("slash path changed: %q %+v", got, result)
	}
	for _, p := range []string{"../bad", "A/../bad", "A/./bad"} {
		d.Path = p
		if e := validateCompactDoc(d); e == nil {
			t.Fatalf("dot segment accepted: %q", p)
		}
	}
}
