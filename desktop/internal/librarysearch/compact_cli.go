package librarysearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

// This reference intentionally accumulates only query-term hits, not the index
// representation. It is for bounded validation samples, never full-corpus builds.
func compactReference(input, expanded string, queries []string, field, collection string, bodyWords int) (map[string]int, error) {
	return compactReferenceAvailable(input, expanded, queries, field, collection, bodyWords, nil)
}
func compactReferenceAvailable(input, expanded string, queries []string, field, collection string, bodyWords int, available map[string]bool) (map[string]int, error) {
	mask, e := maskForField(field)
	if e != nil {
		return nil, e
	}
	terms := map[string]bool{}
	for _, q := range queries {
		for _, w := range words(q) {
			terms[w] = true
		}
	}
	type pageHits struct {
		hits  map[string]byte
		texts map[int]string
	}
	pages := map[string]*pageHits{}
	e = walkCompactSource(input, expanded, func(d doc) error {
		if available != nil && !available[d.Collection] {
			return nil
		}
		if collection != "" && d.Collection != collection {
			return nil
		}
		key := pageKey(d)
		p := pages[key]
		if p == nil {
			if len(pages) >= 100000 {
				return errors.New("reference check limited to 100000 pages; omit source arguments for large benchmarks")
			}
			p = &pageHits{map[string]byte{}, map[int]string{}}
			pages[key] = p
		}
		fields := []struct {
			text string
			mask byte
		}{{d.Title, fieldTitle}, {strings.Join(d.Authors, " "), fieldAuthor}, {strings.Join(d.Subjects, " "), fieldSubject}, {strings.Join(d.Headings, " "), fieldHeading}}
		if bodyWords == 0 {
			fields = append(fields, struct {
				text string
				mask byte
			}{d.Text, fieldBody})
		} else if bodyWords > 0 {
			p.texts[d.Passage] = strings.Join(firstWords(d.Text, bodyWords), " ")
			keys := make([]int, 0, len(p.texts))
			for k := range p.texts {
				keys = append(keys, k)
			}
			sort.Ints(keys)
			left := bodyWords
			for _, k := range keys {
				if left <= 0 {
					delete(p.texts, k)
					continue
				}
				tokens := firstWords(p.texts[k], left)
				p.texts[k] = strings.Join(tokens, " ")
				left -= len(tokens)
			}
		}
		for _, f := range fields {
			for _, w := range words(f.text) {
				if terms[w] {
					p.hits[w] |= f.mask
				}
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	if bodyWords > 0 {
		for _, p := range pages {
			keys := make([]int, 0, len(p.texts))
			for k := range p.texts {
				keys = append(keys, k)
			}
			sort.Ints(keys)
			remaining := bodyWords
			for _, k := range keys {
				ws := strings.Fields(p.texts[k])
				if len(ws) > remaining {
					ws = ws[:remaining]
				}
				for _, w := range words(strings.Join(ws, " ")) {
					if terms[w] {
						p.hits[w] |= fieldBody
					}
				}
				remaining -= len(ws)
				if remaining == 0 {
					break
				}
			}
			p.texts = nil
		}
	}
	counts := map[string]int{}
	for _, q := range queries {
		ws := words(q)
		if len(ws) == 0 {
			continue
		}
		for _, p := range pages {
			yes := true
			for _, w := range ws {
				if p.hits[w]&mask == 0 {
					yes = false
					break
				}
			}
			if yes {
				counts[q]++
			}
		}
	}
	return counts, nil
}

func benchCompact(dir, input, expanded, queueRoot, queryList, field, collection, label string, cacheMiB int, evict bool) error {
	opened := time.Now()
	en, e := openCompact(dir, cacheMiB)
	if e != nil {
		return e
	}
	defer en.close()
	openSeconds := time.Since(opened).Seconds()
	queries := strings.Split(queryList, ",")
	var reference map[string]int
	if input != "" || expanded != "" || queueRoot != "" {
		available := map[string]bool{}
		for name := range en.ranges {
			available[name] = true
		}
		if queueRoot != "" {
			reference, e = catalogReference(queueRoot, field, collection, queries, available)
		} else {
			reference, e = compactReferenceAvailable(input, expanded, queries, field, collection, en.bodyWords, available)
		}
		if e != nil {
			return e
		}
	}
	rows := []map[string]any{}
	for _, q := range queries {
		en.cache = newCache(cacheMiB << 20)
		for _, run := range []string{"empty_app_cache", "warm_app_cache"} {
			if evict && run == "empty_app_cache" {
				for _, file := range en.f.files {
					if e = evictFilePages(file); e != nil {
						return e
					}
				}
			}
			beforeB, beforeR := en.readBytes, en.reads
			beforeP, pe := physicalReadBytes()
			start := time.Now()
			count, docs, e := en.queryFiltered(q, field, collection)
			elapsed := time.Since(start).Seconds() * 1000
			if e != nil {
				return e
			}
			if reference != nil && count != reference[q] {
				return fmt.Errorf("compact query %q got %d want %d", q, count, reference[q])
			}
			afterP, ae := physicalReadBytes()
			var physical any
			if pe == nil && ae == nil {
				physical = afterP - beforeP
			}
			rows = append(rows, map[string]any{"query": q, "run": run, "matches": count, "returned": len(docs), "milliseconds": elapsed, "read_bytes": en.readBytes - beforeB, "reads": en.reads - beforeR, "storage_read_bytes": physical, "cache_bytes": en.cache.used})
		}
	}
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"version": 4, "pages": en.pages, "terms": en.terms, "body_words_per_page": en.bodyWords, "index_bytes": dirBytes(dir), "open_seconds": openSeconds, "heap_alloc_bytes": ms.HeapAlloc, "go_sys_bytes": ms.Sys, "sparse_dictionary_blocks": len(en.directory.Words), "page_blocks": len(en.directory.Pages), "field": field, "collection": collection, "reference_checked": reference != nil, "storage": label, "file_page_eviction_requested": evict, "results": rows})
}

func firstWords(text string, n int) []string {
	ws := strings.Fields(text)
	if len(ws) > n {
		return ws[:n]
	}
	return ws
}
