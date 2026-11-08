package librarysearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMetadataJoinRequiresBookPath(t *testing.T) {
	m := &metadata{Collections: map[string]collectionMetadata{"books": {Label: "Science", Subjects: []string{"Science"}, Source: "catalog"}}, Books: map[string]map[string]bookMetadata{"books": {"42": {Title: "Solar Guide", Author: "A. Writer", Subject: "Q", Source: "book-list#42"}}}}
	good := enrich(doc{Collection: "books", Title: "Solar Guide", Path: "Solar Guide_cover.42"}, m)
	if len(good.Authors) != 1 || good.Authors[0] != "A. Writer" || good.CollectionLabel != "Science" {
		t.Fatalf("bad join %+v", good)
	}
	bad := enrich(doc{Collection: "books", Title: "Someone", Path: "Someone.42"}, m)
	if len(bad.Authors) != 0 {
		t.Fatal("author-directory entry misattributed")
	}
}
func TestVersionTwoFieldsAndCollectionFilter(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "sample")
	f, _ := os.Create(input)
	enc := json.NewEncoder(f)
	for _, d := range []doc{{Collection: "agriculture", Title: "Cheese Making", Path: "Cheese Making_cover.1"}, {Collection: "science", Title: "Solar Cells", Path: "Solar Cells_cover.2"}, {Collection: "unknown", Title: "Solar Repair", Path: "guide"}} {
		enc.Encode(d)
	}
	f.Close()
	m := &metadata{Collections: map[string]collectionMetadata{"agriculture": {Label: "Agriculture", Subjects: []string{"Agriculture"}}, "science": {Label: "Science", Subjects: []string{"Science"}}}, Books: map[string]map[string]bookMetadata{"agriculture": {"1": {Title: "Cheese Making", Author: "Bob Brown", Source: "book#1"}}, "science": {"2": {Title: "Solar Cells", Author: "Ada Brown", Source: "book#2"}}}}
	dir := filepath.Join(root, "index")
	if e := buildEnriched(input, dir, 3, 1, 1024, "flate", m); e != nil {
		t.Fatal(e)
	}
	en, e := openIndex(dir, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer en.close()
	for _, x := range []struct {
		q, field, collection string
		n                    int
	}{{"brown", "author", "", 2}, {"brown", "title", "", 0}, {"agriculture", "subject", "", 1}, {"cheese brown", "all", "", 1}, {"brown", "author", "science", 1}, {"solar", "title", "unknown", 1}, {"brown", "author", "unknown", 0}} {
		n, docs, e := en.queryFiltered(x.q, x.field, x.collection)
		if e != nil || n != x.n || len(docs) != x.n {
			t.Fatalf("%+v got %d/%d %v", x, n, len(docs), e)
		}
	}
}
