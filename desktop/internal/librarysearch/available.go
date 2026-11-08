package librarysearch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Availability is independent of the prebuilt catalog. Downloaded .part files
// do not match the *.zim inventory; completed matching archives appear on refresh.
type availableQuery struct {
	engine    queryEngine
	archives  []archiveIdentity
	labels    map[string]string
	directory string
	mu        sync.Mutex
	refreshed time.Time
	matches   map[string]string
	interval  time.Duration
}

func newAvailableQuery(en queryEngine, indexDir, archiveDir string) (*availableQuery, error) {
	b, e := os.ReadFile(filepath.Join(indexDir, "manifest.json"))
	if e != nil {
		return nil, e
	}
	var m compactManifest
	if e = json.Unmarshal(b, &m); e != nil {
		return nil, e
	}
	if len(m.Archives) == 0 {
		return nil, errors.New("index needs archive identities before availability filtering; rebuild or bind its manifest")
	}
	return &availableQuery{engine: en, archives: m.Archives, labels: m.Collections, directory: archiveDir, interval: 10 * time.Second}, nil
}
func (a *availableQuery) snapshot() (map[string]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.refreshed.IsZero() && time.Since(a.refreshed) < a.interval {
		return a.matches, nil
	}
	st, e := os.Stat(a.directory)
	if e != nil {
		return nil, e
	}
	if !st.IsDir() {
		return nil, errors.New("archive inventory must be a directory")
	}
	matches, _, e := matchArchiveIdentities(a.archives, a.directory)
	if e != nil {
		return nil, e
	}
	a.matches = matches
	a.refreshed = time.Now()
	return matches, nil
}
func (a *availableQuery) collectionAvailability() (map[string]string, error) {
	matches, e := a.snapshot()
	if e != nil {
		return nil, e
	}
	labels := map[string]string{}
	for id := range matches {
		labels[id] = a.labels[id]
	}
	return labels, nil
}
func (a *availableQuery) queryFiltered(q, field, collection string) (int, []doc, error) {
	matches, e := a.snapshot()
	if e != nil {
		return 0, nil, e
	}
	if collection != "" {
		if _, ok := matches[collection]; !ok {
			return 0, nil, nil
		}
	}
	remap := func(docs []doc) []doc {
		for i := range docs {
			docs[i].Collection = matches[docs[i].Collection]
		}
		return docs
	}
	if collection != "" || len(matches) == len(a.labels) {
		n, docs, e := a.engine.queryFiltered(q, field, collection)
		return n, remap(docs), e
	}
	ids := make([]string, 0, len(matches))
	for id := range matches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	total := 0
	var found []doc
	for _, id := range ids {
		count, docs, e := a.engine.queryFiltered(q, field, id)
		if e != nil {
			return 0, nil, e
		}
		total += count
		for _, d := range remap(docs) {
			if len(found) < 20 {
				found = append(found, d)
			}
		}
	}
	return total, found, nil
}
