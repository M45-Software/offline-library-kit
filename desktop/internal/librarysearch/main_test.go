package librarysearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestBinaryResultsMatchReferenceAcrossShards(t *testing.T) {
	for _, codec := range []string{"none", "flate", "adaptive"} {
		t.Run(codec, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "sample.jsonl")
			f, e := os.Create(input)
			if e != nil {
				t.Fatal(e)
			}
			enc := json.NewEncoder(f)
			for _, title := range []string{"Solar generator repair", "Generator oil", "Solar solar repair", "Café WATER", "Water generator", "Oil and oil", "No matches"} {
				enc.Encode(doc{Collection: "test", Title: title, Path: "A/" + title})
			}
			f.Close()
			dir := filepath.Join(root, "index")
			if e = build(input, dir, 7, 2, 1024, codec); e != nil {
				t.Fatal(e)
			}
			en, e := openIndex(dir, 1)
			if e != nil {
				t.Fatal(e)
			}
			defer en.close()
			for _, q := range []string{"solar", "solar generator", "solar solar", "WATER", "café water", "unknown", "oil", ""} {
				want, e := reference(input, q, 7)
				if e != nil {
					t.Fatal(e)
				}
				got, docs, e := en.query(q)
				if e != nil || got != want || len(docs) != want {
					t.Fatalf("query %q got %d/%d want %d: %v", q, got, len(docs), want, e)
				}
				for _, d := range docs {
					ws := words(d.Title)
					for _, word := range words(q) {
						i := sort.SearchStrings(ws, word)
						if i == len(ws) || ws[i] != word {
							t.Fatalf("returned nonmatching document %q for %q", d.Title, q)
						}
					}
					if d.Path != "A/"+d.Title || d.Collection != "test" {
						t.Fatalf("corrupt document record: %+v", d)
					}
				}
				before := en.readBytes
				if _, _, e = en.query(q); e != nil {
					t.Fatal(e)
				}
				if en.readBytes != before {
					t.Fatalf("warm query %q performed reads", q)
				}
			}
		})
	}
}
func TestCorruptPostingDetected(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "shard.bin")
	if e := buildShard(file, []doc{{Title: "solar", Path: "a"}}, 1024, "flate"); e != nil {
		t.Fatal(e)
	}
	m := manifest{Version: 1, Documents: 1, Shards: []string{"shard.bin"}, Codec: "flate"}
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(root, "manifest.json"), b, 0600)
	en, e := openIndex(root, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer en.close()
	off := en.shards[0].terms[0].Loc.Off
	f, e := os.OpenFile(file, os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	var corrupt [1]byte
	if _, e = f.ReadAt(corrupt[:], int64(off)); e != nil {
		t.Fatal(e)
	}
	corrupt[0] ^= 255
	f.WriteAt(corrupt[:], int64(off))
	f.Close()
	if _, _, e = en.query("solar"); e == nil {
		t.Fatal("corrupt posting accepted")
	}
}
func TestCacheStaysWithinPayloadBudget(t *testing.T) {
	c := newCache(10)
	c.add("one", make([]byte, 8))
	c.add("two", make([]byte, 8))
	if c.used > 10 || len(c.items) != 1 {
		t.Fatal("cache exceeds budget")
	}
	if _, ok := c.get("one"); ok {
		t.Fatal("old entry not evicted")
	}
	c.add("large", make([]byte, 30))
	if c.used > 10 {
		t.Fatal("oversized entry cached")
	}
}
