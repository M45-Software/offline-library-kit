package repair

import (
	"bytes"
	"context"
	"offline-library-kit/desktop/internal/zimfixture"
	"os"
	"path/filepath"
	"testing"
)

func TestLinkRepair(t *testing.T) {
	r := newResolver(map[string]int{"Guide/Correct_Title/123": 0, "Device/Case_Name": 1, "Guide/A/777": 2, "Guide/B/777": 3})
	p := []byte(`<html><body><a href="../Guide/Wrong/123?x=1#Step%202">fix</a><a href="../Guide/Wrong/777">ambiguous</a><a href="https://example.com/Guide/Wrong/123">external</a></body></html>`)
	out, n, notice, e := repairHTML("Device/Test", p, r)
	if e != nil || n != 1 || notice {
		t.Fatalf("%d %v %v", n, notice, e)
	}
	if !bytes.Contains(out, []byte(`../Guide/Correct_Title/123?x=1#Step%202`)) {
		t.Fatal(string(out))
	}
	if !bytes.Contains(out, []byte(`../Guide/Wrong/777`)) {
		t.Fatal("ambiguous repaired")
	}
}
func TestCoverageAndIdempotence(t *testing.T) {
	r := newResolver(map[string]int{})
	p := []byte(`<html><body><h1>Empty</h1></body></html>`)
	out, n, notice, e := repairHTML("Device/Empty", p, r)
	if e != nil || n != 0 || !notice {
		t.Fatalf("%v %v", notice, e)
	}
	out2, _, notice2, e := repairHTML("Device/Empty", out, r)
	if e != nil || notice2 || !bytes.Equal(out, out2) {
		t.Fatal("not idempotent")
	}
	_, _, notice, e = repairHTML("Guide/Real/1", []byte(`<body><li class="js-step">Step</li></body>`), r)
	if e != nil || notice {
		t.Fatal("real guide labeled empty")
	}
}
func TestSyntheticArchive(t *testing.T) {
	source := syntheticFixture(t)
	if _, e := os.Stat(source); e != nil {
		t.Skip("generate fixture with make_fixture.py")
	}
	output := filepath.Join(t.TempDir(), "fixed.zim")
	rep, e := run(context.Background(), source, output)
	if e != nil {
		t.Fatal(e)
	}
	if rep.Links != 1 || rep.Notices != 2 {
		t.Fatalf("%+v", rep)
	}
	a, e := openArchive(output)
	if e != nil {
		t.Fatal(e)
	}
	defer a.f.Close()
	if e = a.verify(); e != nil {
		t.Fatal(e)
	}
	if _, e = run(context.Background(), source, output); e == nil {
		t.Fatal("overwrote output")
	}
}

func TestWorkerPoolMatchesSingleWorker(t *testing.T) {
	if _, e := os.Stat(syntheticFixture(t)); e != nil {
		t.Skip("generate fixture")
	}
	var want Report
	for _, workers := range []int{1, 2, 4} {
		out := filepath.Join(t.TempDir(), "fixed.zim")
		got, e := runWithWorkers(context.Background(), syntheticFixture(t), out, workers)
		if e != nil {
			t.Fatal(e)
		}
		if workers == 1 {
			want = got
		}
		if got.Links != want.Links || got.Notices != want.Notices || got.ChangedPages != want.ChangedPages {
			t.Fatalf("worker result differs: %+v", got)
		}
		a, e := openArchive(out)
		if e != nil {
			t.Fatal(e)
		}
		if e = a.verify(); e != nil {
			t.Fatal(e)
		}
		a.f.Close()
	}
}
func TestCanceledWorkerPoolStops(t *testing.T) {
	a, e := openArchive(syntheticFixture(t))
	if e != nil {
		t.Skip(e)
	}
	defer a.f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = repairPages(ctx, a, []int{0}, newResolver(a.paths), 4, func(pageResult) error { return nil }); e == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestEncodedFragmentAndLiteralPlus(t *testing.T) {
	r := newResolver(map[string]int{"Device/A+B": 0})
	out, n, _, e := repairHTML("Info/Test", []byte(`<body><a href="../Device/A+B%23details">target</a></body>`), r)
	if e != nil || n != 1 || !bytes.Contains(out, []byte(`../Device/A+B#details`)) {
		t.Fatalf("%s %d %v", out, n, e)
	}
}

func TestRejectsCorruptSourceChecksum(t *testing.T) {
	b, e := os.ReadFile(syntheticFixture(t))
	if e != nil {
		t.Skip(e)
	}
	b[len(b)-1] ^= 1
	source := filepath.Join(t.TempDir(), "bad.zim")
	os.WriteFile(source, b, 0400)
	out := filepath.Join(t.TempDir(), "result.zim")
	if _, e := Repair(context.Background(), source, out, Options{Workers: 2}); e == nil {
		t.Fatal("corrupt source accepted")
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(after, b) {
		t.Fatal("source changed")
	}
}

func syntheticFixture(t *testing.T) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "fixture.zim")
	if err := zimfixture.Write(name); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestCallbacksStopRepairAndRetainPartial(t *testing.T) {
	source := syntheticFixture(t)
	for _, stage := range []string{"copying", "pages", "checksum", "write"} {
		t.Run(stage, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "fixed.part")
			options := Options{Workers: 2, Progress: func(p Progress) error {
				if p.Phase == stage && (stage != "copying" || p.Done > 0) {
					return context.Canceled
				}
				return nil
			}}
			if stage == "write" {
				options.BeforeWrite = func(int) error { return context.Canceled }
			}
			if _, err := Repair(context.Background(), source, output, options); err != context.Canceled {
				t.Fatalf("%v", err)
			}
			if _, err := os.Stat(output); err != nil {
				t.Fatal("failed output was removed", err)
			}
		})
	}
}

func TestWalkHTMLReadsBookAboveRepairPageBound(t *testing.T) {
	source := filepath.Join(t.TempDir(), "long-book.zim")
	html := append([]byte("<html><body>"), bytes.Repeat([]byte("x"), maxPage)...)
	html = append(html, []byte("</body></html>")...)
	if err := zimfixture.WriteWithHTML(source, string(html)); err != nil {
		t.Fatal(err)
	}
	seen := false
	err := WalkHTML(context.Background(), source, func(path, title string, raw []byte) error {
		if path == "Guide/Correct/123" {
			seen = bytes.Equal(raw, html)
		}
		return nil
	})
	if err != nil || !seen {
		t.Fatalf("long book unreadable: seen=%t err=%v", seen, err)
	}
}
