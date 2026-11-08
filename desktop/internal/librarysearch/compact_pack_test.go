package librarysearch

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMultipartIndexReadsAcrossBoundariesAndMatchesQueries(t *testing.T) {
	_, source := compactFixture(t, 0)
	out := filepath.Join(t.TempDir(), "packed")
	m, e := packCompact(context.Background(), source, out, 257, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(m.Parts) < 2 {
		t.Fatal("fixture did not split")
	}
	if _, e = os.Stat(filepath.Join(out, "index.bin")); !os.IsNotExist(e) {
		t.Fatal("logical index was redundantly joined")
	}
	if e = verifyCompact(context.Background(), out); e != nil {
		t.Fatal(e)
	}
	original, e := openCompact(source, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer original.close()
	packed, e := openCompact(out, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer packed.close()
	for _, field := range []string{"title", "author", "subject", "heading", "text", "all", "deep"} {
		for _, q := range []string{"engine", "generator", "ada", "science", "rareword", "generator rareword", "batteries", "café"} {
			want, _, e := original.queryFiltered(q, field, "")
			if e != nil {
				t.Fatal(e)
			}
			got, _, e := packed.queryFiltered(q, field, "")
			if e != nil || got != want {
				t.Fatalf("%s/%s got %d want %d: %v", field, q, got, want, e)
			}
		}
	}
	all, e := os.ReadFile(filepath.Join(source, "index.bin"))
	if e != nil {
		t.Fatal(e)
	}
	for start := 0; start < len(all); start += 113 {
		n := 600
		if n > len(all)-start {
			n = len(all) - start
		}
		buf := make([]byte, n)
		got, e := packed.f.ReadAt(buf, int64(start))
		if e != nil || got != n || string(buf) != string(all[start:start+n]) {
			t.Fatalf("read across part boundary %d/%d: %d %v", start, n, got, e)
		}
	}
	buf := make([]byte, 10)
	n, e := packed.f.ReadAt(buf, int64(len(all)-3))
	if n != 3 || e != io.EOF {
		t.Fatalf("EOF semantics: %d %v", n, e)
	}
	repack := filepath.Join(t.TempDir(), "repacked")
	if _, e = packCompact(context.Background(), out, repack, 400, nil); e != nil {
		t.Fatal(e)
	}
	if e = verifyCompact(context.Background(), repack); e != nil {
		t.Fatal(e)
	}
}
func TestMultipartMissingChangedAndInvalidParts(t *testing.T) {
	for _, kind := range []string{"changed", "missing", "traversal", "too many"} {
		t.Run(kind, func(t *testing.T) {
			_, source := compactFixture(t, 0)
			out := filepath.Join(t.TempDir(), "packed")
			m, e := packCompact(context.Background(), source, out, 257, nil)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "changed":
				p := filepath.Join(out, m.Parts[0].Name)
				f, _ := os.OpenFile(p, os.O_RDWR, 0600)
				var b [1]byte
				f.ReadAt(b[:], 128)
				b[0] ^= 1
				f.WriteAt(b[:], 128)
				f.Close()
				if e = verifyCompact(context.Background(), out); e == nil || !strings.Contains(e.Error(), "checksum") {
					t.Fatalf("changed bytes accepted: %v", e)
				}
				return
			case "missing":
				os.Remove(filepath.Join(out, m.Parts[1].Name))
			case "traversal":
				m.Parts[0].Name = "../outside"
				atomicJSON(filepath.Join(out, "manifest.json"), m)
			case "too many":
				m.Parts = make([]compactPart, maxCompactParts+1)
				atomicJSON(filepath.Join(out, "manifest.json"), m)
			}
			if en, e := openCompact(out, 0); e == nil {
				en.close()
				t.Fatal("invalid bundle opened")
			}
		})
	}
}
func TestPackRefusesBadSourceHashAndExistingOutput(t *testing.T) {
	_, source := compactFixture(t, 0)
	b, _ := os.ReadFile(filepath.Join(source, "manifest.json"))
	var m compactManifest
	json.Unmarshal(b, &m)
	m.IndexSHA256 = strings.Repeat("0", 64)
	atomicJSON(filepath.Join(source, "manifest.json"), m)
	out := filepath.Join(t.TempDir(), "packed")
	if _, e := packCompact(context.Background(), source, out, 1024, nil); e == nil {
		t.Fatal("bad source hash accepted")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("bad bundle published")
	}
	if _, e := packCompact(context.Background(), source, source, 1024, nil); e == nil {
		t.Fatal("existing output overwritten")
	}
}
