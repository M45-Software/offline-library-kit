package librarysearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type hybridFixture struct {
	h                     *hybridQuery
	source, index, custom string
	server                *httptest.Server
	requests              atomic.Int64
	failed                atomic.Bool
	unsafe                atomic.Bool
	uuids                 map[string]string
}

func makeHybridFixture(t *testing.T, lazy bool) *hybridFixture {
	t.Helper()
	root := t.TempDir()
	f := &hybridFixture{source: filepath.Join(root, "sources"), index: filepath.Join(root, "catalog"), custom: filepath.Join(root, "custom"), uuids: map[string]string{}}
	os.Mkdir(f.source, 0700)
	input := filepath.Join(root, "input.jsonl")
	file, _ := os.Create(input)
	enc := json.NewEncoder(file)
	for i, name := range []string{"native", "custom", "french"} {
		writeTestArchive(t, filepath.Join(f.source, name+".zim"), byte(i*20))
		id, e := readArchiveIdentity(filepath.Join(f.source, name+".zim"), name)
		if e != nil {
			t.Fatal(e)
		}
		f.uuids[name] = id.UUID
		enc.Encode(doc{Collection: name, CollectionLabel: name, Title: "Shared catalog", Authors: []string{"Alice"}, Path: "metadata"})
	}
	file.Close()
	if _, e := buildCompact(context.Background(), input, "", f.index, compactOptions{BodyWords: -1, SortMiB: 1}); e != nil {
		t.Fatal(e)
	}
	if e := bindCompactArchives(f.index, f.source); e != nil {
		t.Fatal(e)
	}
	if !lazy {
		f.buildCustom(t)
	}
	policy := filepath.Join(root, "library.xml")
	xml := fmt.Sprintf(`<library><book path="native.zim" id="%s" title="Native" language="eng" tags="_ftindex:yes"/><book path="french.zim" id="%s" title="French" language="fra" tags="_ftindex:yes"/><book path="custom.zim" id="%s" title="Custom" language="eng" tags="_ftindex:no"/></library>`, f.uuids["native"], f.uuids["french"], f.uuids["custom"])
	os.WriteFile(policy, []byte(xml), 0600)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Path == "/catalog/v2/entries" {
			fmt.Fprintf(w, `<feed><entry><id>urn:uuid:%s</id><language>eng</language><tags>_ftindex:yes</tags><link type="text/html" href="/content/native"/></entry><entry><id>urn:uuid:%s</id><language>fra</language><tags>_ftindex:yes</tags><link type="text/html" href="/content/french"/></entry></feed>`, f.uuids["native"], f.uuids["french"])
			return
		}
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		f.requests.Add(1)
		if f.failed.Load() {
			w.WriteHeader(503)
			return
		}
		ids := r.URL.Query()["books.id"]
		if len(ids) != 1 {
			t.Errorf("languages mixed or wrong sources: %v", ids)
			w.WriteHeader(400)
			return
		}
		if r.URL.Query().Get("format") != "xml" {
			t.Error("not XML")
		}
		name := "native"
		if ids[0] == f.uuids["french"] {
			name = "french"
		} else if ids[0] != f.uuids["native"] {
			t.Errorf("custom collection sent to native engine: %v", ids)
		}
		link := "/content/" + name + "/article"
		if f.unsafe.Load() {
			link = "https://example.com/outside"
		}
		fmt.Fprintf(w, `<rss><channel><totalResults>1,234</totalResults><item><title>Native result</title><link>%s</link><description><b>Lithium</b> search <script>BAD_SCRIPT</script> snippet</description></item></channel></rss>`, link)
	}))
	base, e := openCompact(f.index, 1)
	if e != nil {
		t.Fatal(e)
	}
	h, e := newHybridQuery(base, f.index, f.source, policy, f.custom, f.server.URL)
	if e != nil {
		t.Fatal(e)
	}
	f.h = h
	t.Cleanup(func() { h.close(); base.close(); f.server.Close() })
	return f
}
func (f *hybridFixture) buildCustom(t *testing.T) {
	t.Helper()
	input := filepath.Join(filepath.Dir(f.custom), "custom.jsonl")
	file, _ := os.Create(input)
	json.NewEncoder(file).Encode(doc{Collection: "custom", CollectionLabel: "Custom", Title: "Battery guide", Authors: []string{"Alice"}, Path: "chapter", Text: "lithium controller", Headings: []string{"Charging batteries"}, Passage: 1})
	file.Close()
	if _, e := buildCompact(context.Background(), input, "", f.custom, compactOptions{SortMiB: 1}); e != nil {
		t.Fatal(e)
	}
	if e := bindCompactArchives(f.custom, f.source); e != nil {
		t.Fatal(e)
	}
}
func TestHybridRoutesFieldsAndCombinesDisjointBackends(t *testing.T) {
	f := makeHybridFixture(t, false)
	h := f.h
	out, e := h.queryDetailed(context.Background(), "alice", "author", "")
	if e != nil || out.Count != 3 || f.requests.Load() != 0 {
		t.Fatalf("metadata sent to native: %+v %v", out, e)
	}
	out, e = h.queryDetailed(context.Background(), "lithium", "deep", "")
	if e != nil || out.Count != 2469 || !out.Estimated || len(out.Docs) != 3 || f.requests.Load() != 2 {
		t.Fatalf("hybrid results: %+v %v calls=%d", out, e, f.requests.Load())
	}
	seen := map[string]bool{}
	for _, d := range out.Docs {
		seen[d.Collection] = true
		if strings.Contains(d.Text, "<") || strings.Contains(d.Text, "BAD_SCRIPT") {
			t.Fatal("snippet markup leaked")
		}
	}
	if !seen["custom"] || !seen["native"] || !seen["french"] {
		t.Fatal("backend results lost")
	}
	before := f.requests.Load()
	out, e = h.queryDetailed(context.Background(), "lithium", "text", "custom")
	if e != nil || out.Count != 1 || f.requests.Load() != before || out.Docs[0].Path != "chapter" {
		t.Fatalf("custom routing/cache isolation: %+v %v", out, e)
	}
	if _, e = h.queryDetailed(context.Background(), "lithium", "text", "native"); e == nil {
		t.Fatal("native search mislabeled body-only")
	}
	deep, heading, body, _ := h.searchFeatures("native")
	if !deep || heading || body {
		t.Fatal("native-only field features wrong")
	}
	deep, heading, body, _ = h.searchFeatures("custom")
	if !deep || !heading || !body {
		t.Fatal("custom field features wrong")
	}
}
func TestHybridReportsNativeFailureAndRejectsExternalLinks(t *testing.T) {
	f := makeHybridFixture(t, false)
	f.failed.Store(true)
	out, e := f.h.queryDetailed(context.Background(), "lithium", "deep", "")
	if e != nil || out.Count != 1 || len(out.Docs) != 1 || out.Notice == "" {
		t.Fatalf("working custom results discarded: %+v %v", out, e)
	}
	if _, e = f.h.queryDetailed(context.Background(), "solar", "deep", "native"); !errors.Is(e, errSearchUnavailable) {
		t.Fatalf("native failure silently became no matches: %v", e)
	}
	f.failed.Store(false)
	f.unsafe.Store(true)
	out, e = f.h.queryDetailed(context.Background(), "lithium", "deep", "")
	if e != nil || len(out.Docs) != 1 || out.Docs[0].Collection != "custom" || out.Notice == "" {
		t.Fatalf("unsafe native result accepted: %+v %v", out, e)
	}
}
func TestHybridLoadsCompletedSupplementaryIndexWithoutRestart(t *testing.T) {
	f := makeHybridFixture(t, true)
	out, e := f.h.queryDetailed(context.Background(), "lithium", "deep", "")
	if e != nil || out.Count != 2468 || out.Notice == "" {
		t.Fatalf("native-only early results: %+v %v", out, e)
	}
	d, _, _, _ := f.h.searchFeatures("custom")
	if d {
		t.Fatal("unbuilt custom deep search advertised")
	}
	f.buildCustom(t)
	out, e = f.h.queryDetailed(context.Background(), "lithium", "deep", "custom")
	if e != nil || out.Count != 1 {
		t.Fatalf("completed custom index not activated: %+v %v", out, e)
	}
}
func TestBalancedResultsDeduplicateAndRespectLimit(t *testing.T) {
	a := []doc{{Collection: "one", Path: "a"}, {Collection: "one", Path: "a"}, {Collection: "one", Path: "b"}}
	b := []doc{{Collection: "two", Path: "a"}, {Collection: "two", Path: "b"}}
	out := balancedResults(a, b, 3)
	if len(out) != 3 || out[0].Collection != "one" || out[1].Collection != "two" || out[2].Path != "b" {
		t.Fatalf("unbalanced result merge: %+v", out)
	}
}
