package librarysearch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCompactCatalogQueuesAndStreamingReference(t *testing.T) {
	p, root := sampleFixture(t)
	writeTestArchive(t, p.Queues[0].Source, 0)
	st, _ := os.Stat(p.Queues[0].Source)
	p.Queues[0].SourceBytes = st.Size()
	p.Queues[0].SourceMtime = st.ModTime().UnixNano()
	path := filepath.Join(p.Config.Work, "queues", "test.jsonl")
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	json.NewEncoder(f).Encode(doc{Collection: "test", Title: "My Book", Path: "My Book.7"})
	f.Close()
	st, _ = os.Stat(path)
	p.Queues[0].QueueBytes = st.Size()
	p.Queues[0].Records++
	p.Config.Metadata = filepath.Join(root, "metadata.json")
	m := metadata{Collections: map[string]collectionMetadata{"test": {Label: "Test", Subjects: []string{"Reference"}, Source: "catalog"}}, Books: map[string]map[string]bookMetadata{"test": {"7": {Title: "My Book", Author: "Ada Author", Subject: "T", Source: "book-metadata"}}}}
	if e = atomicJSON(p.Config.Metadata, m); e != nil {
		t.Fatal(e)
	}
	if e = atomicJSON(filepath.Join(p.Config.Work, "plan.json"), p); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(root, "catalog-index")
	stats, e := buildCompact(context.Background(), "", "", out, compactOptions{QueueRoot: p.Config.Work, SortMiB: 1})
	if e != nil {
		t.Fatal(e)
	}
	if stats.Pages != 1001 || stats.BodyWords != -1 {
		t.Fatalf("wrong catalog profile: %+v", stats)
	}
	en, e := openCompact(out, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer en.close()
	for _, test := range []struct {
		q, field string
		n        int
	}{{"page", "title", 1000}, {"ada", "author", 1}, {"reference", "subject", 1001}, {"ada book", "all", 1}, {"page", "text", 0}, {"page", "heading", 0}} {
		count, _, err := en.queryFiltered(test.q, test.field, "")
		if err != nil || count != test.n {
			t.Fatalf("%+v got %d: %v", test, count, err)
		}
		want, err := catalogReference(p.Config.Work, test.field, "", []string{test.q}, map[string]bool{"test": true})
		if err != nil || want[test.q] != count {
			t.Fatalf("reference %q: %v %v", test.q, want, err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(out, "manifest.json"))
	var cm compactManifest
	json.Unmarshal(b, &cm)
	if cm.SourceType != "catalog-queues" {
		t.Fatal("catalog source coverage missing")
	}
	os.Remove(p.Queues[0].Source)
	if e = walkCatalogQueues(p.Config.Work, "", "", func(doc) error { return nil }); e == nil {
		t.Fatal("missing archive was indexed")
	}
}
