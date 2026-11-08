package librarysearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPassagesExcludeBoilerplateAndRetainHeadingAnchor(t *testing.T) {
	raw := `<!doctype html><html><head><title>IGNOREHEAD</title><style>IGNORESTYLE</style></head><body><nav>IGNORENAV</nav><script>IGNORESCRIPT</script><div hidden>IGNOREHIDDEN</div><div id="pg-footer">IGNORELICENSE</div><h1 id="book">Solar Power</h1><h2 id="charging">Charging &amp; batteries</h2><p>Use <b>sun</b>light to recharge cells safely with a controller.</p></body></html>`
	chunks, e := passages(doc{Title: "Solar Power", Collection: "books", Path: "solar"}, []byte(raw), 200, 100)
	if e != nil {
		t.Fatal(e)
	}
	var combined string
	found := false
	for _, d := range chunks {
		combined += " " + d.Text + strings.Join(d.Headings, " ")
		if strings.Contains(d.Text, "sunlight") {
			found = true
			if d.Anchor != "charging" || len(d.Headings) != 2 || d.Headings[1] != "Charging & batteries" {
				t.Fatalf("heading/anchor lost: %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("body passage missing")
	}
	if strings.Contains(combined, "IGNORE") {
		t.Fatalf("boilerplate leaked: %s", combined)
	}
}
func TestDeepBinaryRoundtripAndFieldSeparation(t *testing.T) {
	chunks, e := passages(doc{Collection: "test", Title: "Solar book", Path: "book"}, []byte(`<h2 id="electrical">Charging batteries</h2><p>Recharge lithium cells using sunlight and a suitable controller.</p>`), 200, 100)
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	input := filepath.Join(root, "passages.jsonl")
	f, _ := os.Create(input)
	enc := json.NewEncoder(f)
	for _, d := range chunks {
		enc.Encode(d)
	}
	f.Close()
	dir := filepath.Join(root, "index")
	if e = build(input, dir, 100, 2, 1024, "flate"); e != nil {
		t.Fatal(e)
	}
	en, e := openIndex(dir, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer en.close()
	for _, x := range []struct {
		q, field string
		n        int
	}{{"recharge", "title", 0}, {"recharge", "text", 1}, {"charging", "heading", 1}, {"lithium charging", "deep", 1}} {
		n, docs, e := en.queryFiltered(x.q, x.field, "")
		if e != nil || n != x.n {
			t.Fatalf("%+v got %d %v", x, n, e)
		}
		for _, d := range docs {
			if d.Anchor != "electrical" || d.Passage < 1 || d.Text == "" {
				t.Fatalf("passage metadata lost: %+v", d)
			}
		}
	}
}
func TestPassageLimitIsExplicit(t *testing.T) {
	raw := []byte(`<h2 id="one">Chapter One</h2><p>` + strings.Repeat("searchable words ", 50) + `</p>`)
	chunks, e := passages(doc{Title: "test"}, raw, 10, 2)
	if e != nil || len(chunks) != 2 {
		t.Fatalf("unexpected %d %v", len(chunks), e)
	}
	for _, d := range chunks {
		if !strings.HasPrefix(d.Coverage, "Partial") {
			t.Fatal("truncated extraction not marked")
		}
	}
}
func TestContentRegionAndPrecedingAnchor(t *testing.T) {
	raw := []byte(`<body><p>NOTARTICLE</p><main><a id="chapter-one"></a><h2>Part<br>Two</h2><p>Only the useful chapter text belongs here.</p></main><p>NOTARTICLE</p></body>`)
	chunks, e := passages(doc{Title: "Book"}, raw, 200, 100)
	if e != nil || len(chunks) != 1 {
		t.Fatalf("unexpected chunks %d %v", len(chunks), e)
	}
	d := chunks[0]
	if strings.Contains(d.Text, "NOTARTICLE") || d.Anchor != "chapter-one" || d.Headings[0] != "Part Two" {
		t.Fatalf("bad extraction %+v", d)
	}
}
