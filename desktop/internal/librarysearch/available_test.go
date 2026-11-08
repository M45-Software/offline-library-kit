package librarysearch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPrebuiltCatalogHidesMissingAndChangedArchivesAndRefreshes(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "pages.jsonl")
	f, _ := os.Create(input)
	enc := json.NewEncoder(f)
	for _, c := range []string{"first", "second"} {
		enc.Encode(doc{Collection: c, Path: "entry", Title: "Shared title"})
	}
	f.Close()
	index := filepath.Join(root, "index")
	if _, e := buildCompact(context.Background(), input, "", index, compactOptions{SortMiB: 1}); e != nil {
		t.Fatal(e)
	}
	archives := filepath.Join(root, "archives")
	os.Mkdir(archives, 0700)
	first := filepath.Join(archives, "first.zim")
	second := filepath.Join(archives, "second.zim")
	writeTestArchive(t, first, 0)
	writeTestArchive(t, second, 20)
	if e := bindCompactArchives(index, archives); e != nil {
		t.Fatal(e)
	}
	en, e := openCompact(index, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer en.close()
	a, e := newAvailableQuery(en, index, archives)
	if e != nil {
		t.Fatal(e)
	}
	a.interval = 0
	assertCount := func(want int) {
		t.Helper()
		n, _, e := a.queryFiltered("shared", "title", "")
		if e != nil || n != want {
			t.Fatalf("available matches: %d want %d: %v", n, want, e)
		}
	}
	assertCount(2)
	os.Rename(second, second+".part")
	assertCount(1)
	labels, e := a.collectionAvailability()
	if e != nil || len(labels) != 1 || labels["first"] == "" {
		t.Fatalf("missing archive remained visible: %v %v", labels, e)
	}
	renamed := filepath.Join(archives, "renamed.zim")
	os.Rename(second+".part", renamed)
	assertCount(2)
	n, docs, e := a.queryFiltered("shared", "title", "second")
	if e != nil || n != 1 || len(docs) != 1 || docs[0].Collection != "renamed" {
		t.Fatalf("renamed source routing: %d %+v %v", n, docs, e)
	}
	writeTestArchive(t, renamed, 21)
	assertCount(1)
	n, _, e = a.queryFiltered("shared", "title", "second")
	if e != nil || n != 0 {
		t.Fatal("wrong archive version remained searchable")
	}
	os.Remove(first)
	assertCount(0)
}
