package librarysearch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNativePolicyAndCustomOnlyBuild(t *testing.T) {
	root := t.TempDir()
	xmlPath := filepath.Join(root, "library.xml")
	os.WriteFile(xmlPath, []byte(`<library><book path="wiki.zim" id="one" title="Wiki" language="eng" tags="wikipedia;_ftindex:yes"/><book path="books.zim" id="two" title="Books" language="eng" tags="_ftindex:no"/></library>`), 0600)
	routes, e := readSearchPolicy(xmlPath)
	if e != nil {
		t.Fatal(e)
	}
	selected := customSelection(routes)
	if selected["wiki"] || !selected["books"] || routes["wiki"].Backend != "native" {
		t.Fatalf("wrong routing: %+v", routes)
	}
	input := filepath.Join(root, "input.jsonl")
	f, _ := os.Create(input)
	enc := json.NewEncoder(f)
	for _, name := range []string{"wiki", "books"} {
		enc.Encode(doc{Collection: name, Path: "page", Title: "Solar", Text: "energy", Passage: 1})
	}
	f.Close()
	out := filepath.Join(root, "custom")
	stats, e := buildCompact(context.Background(), input, "", out, compactOptions{SortMiB: 1, Collections: selected})
	if e != nil || stats.Pages != 1 {
		t.Fatalf("custom build: %+v %v", stats, e)
	}
	en, e := openCompact(out, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer en.close()
	n, docs, e := en.queryFiltered("energy", "deep", "")
	if e != nil || n != 1 || docs[0].Collection != "books" {
		t.Fatalf("native indexed twice: %d %+v %v", n, docs, e)
	}
}
func TestHybridSelectionAllowsUnfinishedNativeButNotCustom(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "collections"), 0700)
	os.WriteFile(filepath.Join(root, "reader-library.xml"), []byte(`<library><book path="wiki.zim" tags="_ftindex:yes"/><book path="books.zim" tags="_ftindex:no"/></library>`), 0600)
	p := expandPlan{Version: 1, Queues: []sourceQueue{{Name: "wiki"}, {Name: "books"}}}
	atomicJSON(filepath.Join(root, "plan.json"), p)
	if _, e := prepareHybridSelection(root); e == nil {
		t.Fatal("unfinished custom extraction accepted")
	}
	dir := filepath.Join(root, "collections", "books", "batch-000001")
	os.MkdirAll(dir, 0700)
	atomicJSON(filepath.Join(dir, "commit.json"), batchCommit{After: expandState{Batch: 1, Done: true}})
	selected, e := prepareHybridSelection(root)
	if e != nil || selected["wiki"] || !selected["books"] {
		t.Fatalf("unfinished native blocked reuse: %v %v", selected, e)
	}
}
