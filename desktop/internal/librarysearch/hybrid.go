package librarysearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var errSearchUnavailable = errors.New("full-text search unavailable")

type searchOutcome struct {
	Count     int
	Docs      []doc
	Estimated bool
	Notice    string
}
type hybridQuery struct {
	mu             sync.Mutex
	catalog        *compactEngine
	available      *availableQuery
	routes         map[string]collectionRoute
	native         *nativeClient
	customPath     string
	custom         *compactEngine
	customManifest compactManifest
	shared         *cache
}

func newHybridQuery(base *compactEngine, indexDir, sourceDir, catalogXML, customDir, reader string) (*hybridQuery, error) {
	if sourceDir == "" || catalogXML == "" {
		return nil, errors.New("hybrid search requires -sources and -catalog")
	}
	available, e := newAvailableQuery(base, indexDir, sourceDir)
	if e != nil {
		return nil, e
	}
	routes, e := readSearchPolicy(catalogXML)
	if e != nil {
		return nil, e
	}
	native, e := newNativeClient(reader)
	if e != nil {
		return nil, e
	}
	for _, id := range available.archives {
		r, ok := routes[id.Collection]
		if !ok {
			return nil, fmt.Errorf("search policy missing %s", id.Collection)
		}
		if r.UUID != "" && r.UUID != id.UUID {
			return nil, fmt.Errorf("search policy has a different archive version for %s", id.Collection)
		}
		r.UUID = id.UUID
		routes[id.Collection] = r
	}
	budget := base.cache.max
	if budget > 64<<20 {
		budget = 64 << 20
	}
	shared := newCache(budget)
	base.cache = shared
	h := &hybridQuery{catalog: base, available: available, routes: routes, native: native, customPath: customDir, shared: shared}
	if e = h.loadCustom(); e != nil {
		return nil, e
	}
	return h, nil
}
func (h *hybridQuery) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.custom != nil {
		h.custom.close()
	}
}
func (h *hybridQuery) loadCustom() error {
	if h.custom != nil || h.customPath == "" {
		return nil
	}
	path := filepath.Join(h.customPath, "manifest.json")
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var m compactManifest
	if e = json.Unmarshal(b, &m); e != nil {
		return e
	}
	if m.SourceType == "catalog-queues" {
		return errors.New("supplementary index contains catalog metadata only")
	}
	expected := map[string]archiveIdentity{}
	for _, id := range h.available.archives {
		expected[id.Collection] = id
	}
	bound := map[string]archiveIdentity{}
	for _, id := range m.Archives {
		bound[id.Collection] = id
	}
	for name := range m.Collections {
		if h.routes[name].Backend != "custom" {
			continue
		}
		a, ok := bound[name]
		b, known := expected[name]
		if !ok || !known || a.UUID != b.UUID || a.Bytes != b.Bytes || a.ChecksumMD5 != b.ChecksumMD5 {
			return fmt.Errorf("supplementary index source mismatch: %s", name)
		}
	}
	en, e := openCompact(h.customPath, 0)
	if e != nil {
		return e
	}
	en.cache = h.shared
	h.custom = en
	h.customManifest = m
	return nil
}
func (h *hybridQuery) collectionAvailability() (map[string]string, error) {
	return h.available.collectionAvailability()
}
func (h *hybridQuery) searchFeatures(collection string) (bool, bool, bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	err := h.loadCustom()
	if err != nil {
		return true, false, false, "Some full-text indexes are unavailable; title and metadata search remains available."
	}
	if collection != "" && h.routes[collection].Backend == "native" {
		return true, false, false, "Full-text search uses this collection’s existing index."
	}
	if collection != "" {
		if h.custom == nil {
			return false, false, false, "Title and metadata search is ready. The full-text index is being prepared."
		}
		if _, ok := h.customManifest.Collections[collection]; !ok {
			return false, false, false, "Title and metadata search is ready. No supplementary text index is installed for this collection."
		}
		if h.custom.bodyWords > 0 {
			return true, true, true, fmt.Sprintf("Chapter headings and the first %d body words per page or book are searchable.", h.custom.bodyWords)
		}
		return true, true, h.custom.bodyWords >= 0, "Chapter headings and extracted text are searchable in this collection."
	}
	if h.custom == nil {
		return true, false, false, "Deep search uses existing collection indexes. Additional text indexes are being prepared."
	}
	return true, false, false, "Deep search combines existing collection search with supplementary text indexes. Matching and result counts can vary between collections."
}
func (h *hybridQuery) queryFiltered(q, field, collection string) (int, []doc, error) {
	out, e := h.queryDetailed(context.Background(), q, field, collection)
	return out.Count, out.Docs, e
}
func (h *hybridQuery) queryDetailed(ctx context.Context, q, field, collection string) (searchOutcome, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, e := maskForField(field); e != nil {
		return searchOutcome{}, e
	}
	if len(words(q)) == 0 {
		return searchOutcome{}, nil
	}
	if field == "title" || field == "author" || field == "subject" || field == "all" {
		n, d, e := h.available.queryFiltered(q, field, collection)
		return searchOutcome{Count: n, Docs: d}, e
	}
	matches, e := h.available.snapshot()
	if e != nil {
		return searchOutcome{}, e
	}
	if collection != "" {
		if _, ok := matches[collection]; !ok {
			return searchOutcome{}, nil
		}
	}
	var nativeIDs, customIDs []string
	for id := range matches {
		if collection != "" && id != collection {
			continue
		}
		if h.routes[id].Backend == "native" {
			nativeIDs = append(nativeIDs, id)
		} else {
			customIDs = append(customIDs, id)
		}
	}
	sort.Strings(nativeIDs)
	sort.Strings(customIDs)
	if field != "deep" && (collection == "" || len(nativeIDs) > 0) {
		return searchOutcome{}, errors.New("select a supplementary collection for separate heading or body search")
	}
	var warnings []string
	if e = h.loadCustom(); e != nil {
		warnings = append(warnings, "Some supplementary text indexes could not be opened.")
	}
	out := searchOutcome{}
	var customDocs []doc
	customReady := false
	if len(customIDs) > 0 {
		if h.custom == nil {
			warnings = append(warnings, "Supplementary full-text indexes are still being prepared.")
		} else {
			allCustom := len(customIDs) == len(h.customManifest.Collections)
			for _, id := range customIDs {
				if _, ok := h.customManifest.Collections[id]; !ok {
					allCustom = false
				}
			}
			if allCustom {
				n, d, e := h.custom.queryFiltered(q, field, "")
				if e != nil {
					return out, e
				}
				out.Count += n
				for i := range d {
					d[i].Collection = matches[d[i].Collection]
				}
				customDocs = d
				customReady = true
			} else {
				for _, id := range customIDs {
					if _, ok := h.customManifest.Collections[id]; !ok {
						warnings = append(warnings, "Some supplementary collections have no text index yet.")
						continue
					}
					n, d, e := h.custom.queryFiltered(q, field, id)
					if e != nil {
						return out, e
					}
					out.Count += n
					customReady = true
					for _, row := range d {
						row.Collection = matches[id]
						if len(customDocs) < 20 {
							customDocs = append(customDocs, row)
						}
					}
				}
			}
		}
	}
	var nativeDocs []doc
	nativeReady := false
	if len(nativeIDs) > 0 {
		out.Estimated = true
		books, e := h.native.inventory(ctx)
		if e != nil {
			warnings = append(warnings, "Built-in collection search is temporarily unavailable.")
		} else {
			groups := map[string][]collectionRoute{}
			for _, id := range nativeIDs {
				r := h.routes[id]
				book, ok := books[r.UUID]
				if !ok || !book.FullText {
					warnings = append(warnings, "Some collections are not ready for full-text search in the reader.")
					continue
				}
				lang := book.Language
				if lang == "" {
					lang = r.Language
				}
				if lang == "" {
					lang = "und:" + id
				}
				groups[lang] = append(groups[lang], r)
			}
			languages := make([]string, 0, len(groups))
			for lang := range groups {
				languages = append(languages, lang)
			}
			sort.Strings(languages)
			for _, lang := range languages {
				n, d, err := h.native.search(ctx, q, groups[lang], books)
				if err != nil {
					warnings = append(warnings, "Some built-in collection searches failed; other results are shown.")
					continue
				}
				out.Count += n
				nativeReady = true
				nativeDocs = append(nativeDocs, d...)
			}
		}
	}
	if !customReady && !nativeReady && len(nativeIDs)+len(customIDs) > 0 {
		return out, fmt.Errorf("%w: %s", errSearchUnavailable, strings.Join(uniqueStrings(warnings), " "))
	}
	out.Docs = balancedResults(nativeDocs, customDocs, 20)
	out.Notice = strings.Join(uniqueStrings(warnings), " ")
	return out, nil
}
func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
func balancedResults(a, b []doc, limit int) []doc {
	var out []doc
	seen := map[string]bool{}
	for i := 0; i < len(a) || i < len(b); i++ {
		for _, list := range [][]doc{a, b} {
			if i >= len(list) {
				continue
			}
			d := list[i]
			key := d.Collection + "\x00" + d.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, d)
			if len(out) == limit {
				return out
			}
		}
	}
	return out
}
