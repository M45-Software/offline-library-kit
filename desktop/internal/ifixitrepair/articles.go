package repair

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

// WalkHTML streams real archived HTML for local search construction. The same
// bounded ZIM decoder used by repair handles compression; no external tools run.
func WalkHTML(ctx context.Context, source string, visit func(path, title string, html []byte) error) error {
	a, err := openArchiveContext(ctx, source)
	if err != nil {
		return err
	}
	defer a.f.Close()
	before, err := a.f.Stat()
	if err != nil {
		return err
	}
	indexes := []int{}
	for i, en := range a.entries {
		if !en.redirect && (en.ns == 'A' || en.ns == 'C') && a.mimes[en.mime] == "text/html" {
			indexes = append(indexes, i)
		}
	}
	sort.Slice(indexes, func(i, j int) bool {
		x, y := a.entries[indexes[i]], a.entries[indexes[j]]
		if x.cluster != y.cluster {
			return x.cluster < y.cluster
		}
		return x.blob < y.blob
	})
	for _, i := range indexes {
		if err := ctx.Err(); err != nil {
			return err
		}
		en := a.entries[i]
		r := bufio.NewReader(io.NewSectionReader(a.f, en.offset+16, 8192))
		if _, err = r.ReadString(0); err != nil {
			return err
		}
		title, err := r.ReadString(0)
		if err != nil {
			return err
		}
		title = title[:len(title)-1]
		if title == "" {
			title = en.path
		}
		// Long Gutenberg books can exceed the repairer's 16 MiB page bound.
		// Indexing may use the existing 128 MiB decoded-cluster bound; this
		// does not increase decoder allocation or the repair worker's bound.
		html, err := a.contentWithLimit(en, maxCluster)
		if err != nil {
			return fmt.Errorf("cannot read archived HTML %q: %w", en.path, err)
		}
		if err = visit(en.path, title, html); err != nil {
			return err
		}
	}
	after, err := os.Stat(source)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return errors.New("archive changed while preparing search")
	}
	return nil
}
