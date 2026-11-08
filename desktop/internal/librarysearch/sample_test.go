package librarysearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sampleFixture(t *testing.T) (expandPlan, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if e := os.MkdirAll(filepath.Join(source, "queues"), 0700); e != nil {
		t.Fatal(e)
	}
	c := expandConfig{Work: source, Catalog: filepath.Join(root, "catalog.xml"), Workers: 2, checkVolume: func() error { return nil }}
	os.WriteFile(c.Catalog, []byte(`<library><book path="test.zim" title="Test"/></library>`), 0600)
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	for i := 0; i < 1000; i++ {
		enc.Encode(doc{Collection: "test", Path: fmt.Sprintf("page%d", i), Title: fmt.Sprintf("Page %d", i)})
	}
	q := sourceQueue{Name: "test", Source: filepath.Join(root, "test.zim"), QueueBytes: int64(buf.Len()), Records: 1000}
	if e := os.WriteFile(filepath.Join(source, "queues", "test.jsonl"), []byte(buf.String()), 0600); e != nil {
		t.Fatal(e)
	}
	p := expandPlan{Version: 1, Config: c, Queues: []sourceQueue{q}, InputSHA256: "original-title-scan"}
	if e := atomicJSON(filepath.Join(source, "plan.json"), p); e != nil {
		t.Fatal(e)
	}
	return p, root
}
func TestUniformSampleDeterministicAndSpansQueue(t *testing.T) {
	p, root := sampleFixture(t)
	a, e := prepareSample(context.Background(), p, filepath.Join(root, "a"), 100, 73)
	if e != nil {
		t.Fatal(e)
	}
	b, e := prepareSample(context.Background(), p, filepath.Join(root, "b"), 100, 73)
	if e != nil {
		t.Fatal(e)
	}
	c, e := prepareSample(context.Background(), p, filepath.Join(root, "c"), 100, 74)
	if e != nil {
		t.Fatal(e)
	}
	read := func(dir string) []byte {
		data, e := os.ReadFile(filepath.Join(dir, "queues", "test.jsonl"))
		if e != nil {
			t.Fatal(e)
		}
		return data
	}
	if !reflect.DeepEqual(read(a.Config.Work), read(b.Config.Work)) {
		t.Fatal("same seed changed selection")
	}
	if reflect.DeepEqual(read(a.Config.Work), read(c.Config.Work)) {
		t.Fatal("different seeds produced identical selection")
	}
	s := a.Sampling.Collections[0]
	if s.Selected != 100 || s.Population != 1000 || s.FirstPosition >= 100 || s.LastPosition < 900 {
		t.Fatalf("wrong source coverage: %+v", s)
	}
	for _, count := range s.Deciles {
		if count == 0 {
			t.Fatalf("source decile not sampled: %+v", s)
		}
	}
	if a.Queues[0].Records != 100 || a.InputSHA256 != p.InputSHA256 || a.Sampling.SourcePlanSHA256 == "" || a.Queues[0].QueueBytes != int64(len(read(a.Config.Work))) {
		t.Fatal("incorrect sample plan identity")
	}
	if _, e = prepareSample(context.Background(), p, a.Config.Work, 100, 73); e == nil {
		t.Fatal("existing sample overwritten")
	}
	all, e := prepareSample(context.Background(), p, filepath.Join(root, "all"), 1024, 73)
	if e != nil || all.Queues[0].Records != 1000 {
		t.Fatalf("small collection not retained: %+v %v", all, e)
	}
}
func TestSampleSourceChangeAndCancellation(t *testing.T) {
	p, root := sampleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := filepath.Join(root, "cancelled")
	if _, e := prepareSample(ctx, p, out, 100, 1); e == nil {
		t.Fatal("cancellation ignored")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("cancelled sample published")
	}
	path := filepath.Join(p.Config.Work, "queues", "test.jsonl")
	os.WriteFile(path, []byte("changed"), 0600)
	if _, e := prepareSample(context.Background(), p, filepath.Join(root, "changed"), 100, 1); e == nil {
		t.Fatal("changed queue accepted")
	}
}
