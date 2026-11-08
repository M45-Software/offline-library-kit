package librarysearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func compactFixture(t *testing.T, bodyWords int) (string, string) {
	t.Helper()
	root := t.TempDir()
	input := filepath.Join(root, "source.jsonl")
	f, e := os.Create(input)
	if e != nil {
		t.Fatal(e)
	}
	enc := json.NewEncoder(f)
	// Deliberately noncontiguous and out of passage order: page metadata and
	// word postings must be merged rather than duplicated or silently dropped.
	for _, d := range []doc{
		{Collection: "books", Path: "one", Title: "Solar engine", Authors: []string{"Ada"}, Subjects: []string{"Science"}, Passage: 2, Text: "rareword lithium", Headings: []string{"Charging batteries"}},
		{Collection: "other", Path: "one", Title: "Oil engine", Passage: 1, Text: "generator engine"},
		{Collection: "books", Path: "two", Title: "Water book", Authors: []string{"Bob"}, Passage: 1, Text: "recharge café"},
		{Collection: "books", Path: "one", Title: "Solar engine", Authors: []string{"Ada"}, Subjects: []string{"Science"}, Passage: 1, Text: "generator repair"},
	} {
		if e = enc.Encode(d); e != nil {
			t.Fatal(e)
		}
	}
	f.Close()
	out := filepath.Join(root, "index")
	stats, e := buildCompact(context.Background(), input, "", out, compactOptions{BodyWords: bodyWords, SortMiB: 1})
	if e != nil {
		t.Fatal(e)
	}
	if stats.InputPassages != 4 || stats.Pages != 3 {
		t.Fatalf("wrong counts: %+v", stats)
	}
	return input, out
}
func TestCompactFieldMasksPageANDAndSelectiveBody(t *testing.T) {
	for _, limit := range []int{0, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			input, out := compactFixture(t, limit)
			en, e := openCompact(out, 1)
			if e != nil {
				t.Fatal(e)
			}
			defer en.close()
			tests := []struct {
				q, field, col string
				want          int
			}{{"engine", "title", "", 2}, {"engine", "text", "", 1}, {"ada science", "all", "", 1}, {"ada generator", "deep", "", 1}, {"batteries", "heading", "", 1}, {"batteries", "title", "", 0}, {"engine", "title", "books", 1}, {"engine", "title", "missing", 0}, {"café", "text", "", 1}, {"solar solar", "deep", "", 1}, {"unknown", "deep", "", 0}, {"", "deep", "", 0}}
			if limit == 0 {
				tests = append(tests, struct {
					q, field, col string
					want          int
				}{"generator rareword", "text", "", 1})
			} else {
				tests = append(tests, struct {
					q, field, col string
					want          int
				}{"rareword", "text", "", 0})
			}
			for _, x := range tests {
				count, docs, e := en.queryFiltered(x.q, x.field, x.col)
				if e != nil || count != x.want {
					t.Fatalf("%+v got %d %v", x, count, e)
				}
				for _, d := range docs {
					if d.Text != "" || d.Anchor != "" || d.Passage != 0 {
						t.Fatalf("stored redundant passage data: %+v", d)
					}
				}
				reference, e := compactReference(input, "", []string{x.q}, x.field, x.col, limit)
				if e != nil || count != reference[x.q] {
					t.Fatalf("reference %q: %d/%d %v", x.q, count, reference[x.q], e)
				}
			}
			if _, _, e = en.queryFiltered("engine", "invalid", ""); e == nil {
				t.Fatal("invalid field accepted")
			}
		})
	}
}
func TestCompactCorruptionDetected(t *testing.T) {
	for _, kind := range []string{"header", "directory", "dictionary", "page"} {
		t.Run(kind, func(t *testing.T) {
			_, dir := compactFixture(t, 0)
			en, e := openCompact(dir, 0)
			if e != nil {
				t.Fatal(e)
			}
			var off uint64
			switch kind {
			case "header":
				off = 28
			case "directory":
				off = en.dirStart
			case "dictionary":
				off = en.directory.Words[0].Loc.Off
			case "page":
				off = en.directory.Pages[0].Loc.Off
			default:
				term, ok, err := en.find("generator")
				if err != nil || !ok {
					t.Fatal(err)
				}
				off = term.Off
				if kind == "posting payload" {
					off += 28
				}
			}
			en.close()
			f, e := os.OpenFile(filepath.Join(dir, "index.bin"), os.O_RDWR, 0600)
			if e != nil {
				t.Fatal(e)
			}
			var b [1]byte
			f.ReadAt(b[:], int64(off))
			b[0] ^= 128
			f.WriteAt(b[:], int64(off))
			f.Close()
			en, e = openCompact(dir, 0)
			if e == nil {
				defer en.close()
				_, _, e = en.queryFiltered("generator", "text", "")
			}
			if e == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
}
func TestCompactLargePostingListSkipsAndIntersects(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input.jsonl")
	f, e := os.Create(input)
	if e != nil {
		t.Fatal(e)
	}
	enc := json.NewEncoder(f)
	const n = 17000
	for i := 0; i < n; i++ {
		text := "common"
		if i == n-1 {
			text += " needle"
		}
		enc.Encode(doc{Collection: "test", Path: fmt.Sprintf("page%05d", i), Title: "Record", Text: text, Passage: 1})
	}
	f.Close()
	out := filepath.Join(root, "index")
	if _, e = buildCompact(context.Background(), input, "", out, compactOptions{SortMiB: 1}); e != nil {
		t.Fatal(e)
	}
	en, e := openCompact(out, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer en.close()
	for _, x := range []struct {
		q     string
		count int
	}{{"common", n}, {"common needle", 1}, {"needle common", 1}, {"record common", n}} {
		got, _, e := en.queryFiltered(x.q, "deep", "")
		if e != nil || got != x.count {
			t.Fatalf("%q got %d want %d: %v", x.q, got, x.count, e)
		}
	}
	if en.cache.used > 1<<20 {
		t.Fatal("cache exceeded budget")
	}
}
func TestDiskSorterBoundedFanIn(t *testing.T) {
	root := t.TempDir()
	s := newDiskSorter(context.Background(), root, "test", 64)
	for i := 150; i >= 0; i-- {
		if e := s.add(fmt.Sprintf("%05d", i), []byte("value")); e != nil {
			t.Fatal(e)
		}
	}
	next := 0
	e := s.finish(func(r sortRecord) error {
		if r.key != fmt.Sprintf("%05d", next) {
			return fmt.Errorf("wrong merge order %q at %d", r.key, next)
		}
		next++
		return nil
	})
	if e != nil || next != 151 {
		t.Fatalf("merged %d: %v", next, e)
	}
	files, _ := os.ReadDir(root)
	if len(files) != 0 {
		t.Fatal("temporary runs not removed")
	}
}
func TestCompactFailureDoesNotPublish(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "bad.jsonl")
	os.WriteFile(input, []byte(`{"collection":"test","path":"../bad","title":"bad"}`+"\n"), 0600)
	out := filepath.Join(root, "index")
	if _, e := buildCompact(context.Background(), input, "", out, compactOptions{SortMiB: 1}); e == nil {
		t.Fatal("invalid path accepted")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("failed build published")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := buildCompact(ctx, input, "", out, compactOptions{SortMiB: 1}); e == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestCompactEmptyAndExistingOutput(t *testing.T) {
	_, out := compactFixture(t, 0)
	if _, e := buildCompact(context.Background(), "missing", "", out, compactOptions{SortMiB: 1}); e == nil || !strings.Contains(e.Error(), "new directory") {
		t.Fatalf("existing output accepted: %v", e)
	}
}

func TestCompactExternalPostingCorruptionDetected(t *testing.T) {
	for _, delta := range []uint64{0, 28} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "input.jsonl")
			f, _ := os.Create(input)
			enc := json.NewEncoder(f)
			for i := 0; i < 70; i++ {
				enc.Encode(doc{Collection: "test", Path: fmt.Sprintf("p%03d", i), Title: "Common", Passage: 1})
			}
			f.Close()
			out := filepath.Join(root, "index")
			if _, e := buildCompact(context.Background(), input, "", out, compactOptions{SortMiB: 1}); e != nil {
				t.Fatal(e)
			}
			en, e := openCompact(out, 0)
			if e != nil {
				t.Fatal(e)
			}
			defer en.close()
			term, ok, e := en.find("common")
			if e != nil || !ok || len(term.Inline) > 0 {
				t.Fatalf("expected external postings: %+v %v", term, e)
			}
			f, _ = os.OpenFile(filepath.Join(out, "index.bin"), os.O_RDWR, 0600)
			off := int64(term.Off + delta)
			var b [1]byte
			f.ReadAt(b[:], off)
			b[0] ^= 128
			f.WriteAt(b[:], off)
			f.Close()
			en.cache = newCache(0)
			if _, _, e = en.queryFiltered("common", "title", ""); e == nil {
				t.Fatal("posting corruption accepted")
			}
		})
	}
}

func TestCompactMetadataOnlyProfileAndBundleHash(t *testing.T) {
	_, dir := compactFixture(t, -1)
	en, e := openCompact(dir, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer en.close()
	for _, x := range []struct {
		q, field string
		want     int
	}{{"engine", "title", 2}, {"generator", "text", 0}, {"batteries", "heading", 1}, {"ada science", "all", 1}} {
		got, _, err := en.queryFiltered(x.q, x.field, "")
		if err != nil || got != x.want {
			t.Fatalf("%+v got %d: %v", x, got, err)
		}
	}
	b, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	var m compactManifest
	if e = json.Unmarshal(b, &m); e != nil {
		t.Fatal(e)
	}
	b, e = os.ReadFile(filepath.Join(dir, "index.bin"))
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	if m.IndexSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("published bundle hash mismatch")
	}
}

func TestCompactBuildSingleCollection(t *testing.T) {
	input, _ := compactFixture(t, 0)
	out := filepath.Join(t.TempDir(), "books-only")
	stats, e := buildCompact(context.Background(), input, "", out, compactOptions{SortMiB: 1, Collection: "books"})
	if e != nil || stats.Pages != 2 {
		t.Fatalf("collection build: %+v %v", stats, e)
	}
	en, e := openCompact(out, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer en.close()
	if len(en.ranges) != 1 {
		t.Fatal("unselected collection included")
	}
	n, _, e := en.queryFiltered("engine", "title", "")
	if e != nil || n != 1 {
		t.Fatalf("selected search: %d %v", n, e)
	}
}
