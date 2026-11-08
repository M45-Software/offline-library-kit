package librarysearch

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The selected queues already enumerate nonempty HTML entries. This path builds
// basic search without fetching or storing their bodies.
func walkCatalogQueues(root, collection, metadataPath string, visit func(doc) error) error {
	b, e := os.ReadFile(filepath.Join(root, "plan.json"))
	if e != nil {
		return e
	}
	var p expandPlan
	if e = json.Unmarshal(b, &p); e != nil {
		return e
	}
	if p.Version != 1 || len(p.Queues) == 0 {
		return errors.New("invalid catalog queue plan")
	}
	if metadataPath == "" {
		metadataPath = p.Config.Metadata
	}
	meta, e := loadMetadata(metadataPath)
	if e != nil {
		return e
	}
	for _, q := range p.Queues {
		if collection != "" && q.Name != collection {
			continue
		}
		if filepath.Base(q.Name) != q.Name || q.Name == "." || q.Name == ".." {
			return errors.New("invalid catalog collection")
		}
		st, e := os.Stat(q.Source)
		if e != nil || st.Size() != q.SourceBytes || st.ModTime().UnixNano() != q.SourceMtime {
			return fmt.Errorf("catalog archive changed or unavailable: %s", q.Name)
		}
		path := filepath.Join(root, "queues", q.Name+".jsonl")
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		before, e := f.Stat()
		if e != nil || before.Size() != q.QueueBytes {
			f.Close()
			return errors.New("catalog queue changed")
		}
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 65536), 4<<20)
		var count int64
		for s.Scan() {
			var d doc
			if e = json.Unmarshal(s.Bytes(), &d); e != nil {
				f.Close()
				return e
			}
			if d.Collection != q.Name || d.Passage != 0 {
				f.Close()
				return errors.New("catalog queue schema mismatch")
			}
			d.Text = ""
			d.Headings = nil
			d.Anchor = ""
			d = enrich(d, meta)
			if e = visit(d); e != nil {
				f.Close()
				return e
			}
			count++
		}
		e = s.Err()
		after, se := f.Stat()
		f.Close()
		if e != nil {
			return e
		}
		if se != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || count != q.Records {
			return errors.New("catalog queue changed during read")
		}
	}
	return nil
}
func catalogReference(root, field, collection string, queries []string, available map[string]bool) (map[string]int, error) {
	if _, e := maskForField(field); e != nil {
		return nil, e
	}
	counts := map[string]int{}
	queryWords := make([][]string, len(queries))
	for i, q := range queries {
		queryWords[i] = words(q)
	}
	e := walkCatalogQueues(root, collection, "", func(d doc) error {
		if !available[d.Collection] {
			return nil
		}
		var text string
		switch field {
		case "title":
			text = d.Title
		case "author":
			text = strings.Join(d.Authors, " ")
		case "subject":
			text = strings.Join(d.Subjects, " ")
		case "all", "deep":
			text = d.Title + " " + strings.Join(d.Authors, " ") + " " + strings.Join(d.Subjects, " ")
		case "heading", "text":
			return nil
		}
		ws := words(text)
		for i, qw := range queryWords {
			if len(qw) == 0 {
				continue
			}
			match := true
			for _, w := range qw {
				n := sort.SearchStrings(ws, w)
				if n == len(ws) || ws[n] != w {
					match = false
					break
				}
			}
			if match {
				counts[queries[i]]++
			}
		}
		return nil
	})
	return counts, e
}
