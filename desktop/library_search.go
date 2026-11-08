package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type localCollection struct {
	Kind            string `json:"kind"`
	Topic           string `json:"topic"`
	IndexedTitles   bool   `json:"indexedTitles,omitempty"`
	CustomText      bool   `json:"customText,omitempty"`
	ID              string `json:"id"`
	Name            string `json:"name"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Language        string `json:"language"`
	FullText        bool   `json:"fullText"`
	IndexStatus     string `json:"indexStatus"`
	AddressConflict bool   `json:"addressConflict"`
}
type localSearchResult struct {
	Snippet    string   `json:"snippet,omitempty"`
	Authors    []string `json:"authors,omitempty"`
	Subjects   []string `json:"subjects,omitempty"`
	Title      string   `json:"title"`
	Collection string   `json:"collection"`
	URL        string   `json:"url"`
	MatchMode  string   `json:"matchMode,omitempty"`
	rank       int
}
type localCatalogFeed struct {
	Entries []struct {
		ID       string        `xml:"id"`
		Title    string        `xml:"title"`
		Summary  string        `xml:"summary"`
		Language string        `xml:"language"`
		Tags     string        `xml:"tags"`
		Links    []catalogLink `xml:"link"`
	} `xml:"entry"`
}

func (a *app) localReaderURL() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.readerCmd == nil || a.readerURL == "" {
		return "", errors.New("Start the reader before searching")
	}
	return a.readerURL, nil
}
func (a *app) localCollections(ctx context.Context) ([]localCollection, error) {
	base, err := a.localReaderURL()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+"catalog/v2/entries?count=-1", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Reader is still starting: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("Reader catalog is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8*1024*1024 {
		return nil, errors.New("Reader catalog is too large")
	}
	var feed localCatalogFeed
	if err = xml.Unmarshal(data, &feed); err != nil {
		return nil, err
	}
	items := make([]localCollection, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		id := strings.TrimPrefix(e.ID, "urn:uuid:")
		if !uuidPattern.MatchString(id) {
			continue
		}
		var alias string
		for _, link := range e.Links {
			if link.Type != "text/html" {
				continue
			}
			u, err := url.Parse(link.Href)
			if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/content/") {
				continue
			}
			alias = strings.TrimPrefix(u.Path, "/content/")
			if alias == "" || strings.Contains(alias, "/") {
				alias = ""
				continue
			}
			break
		}
		if alias == "" {
			continue
		}
		indexStatus := "unknown"
		for _, tag := range strings.Split(e.Tags, ";") {
			if tag == "_ftindex:yes" {
				indexStatus = "yes"
			}
			if tag == "_ftindex:no" {
				indexStatus = "no"
			}
		}
		items = append(items, localCollection{ID: id, Name: alias, Title: e.Title, Description: e.Summary, Language: e.Language, FullText: indexStatus != "no", IndexStatus: indexStatus})
	}
	a.markCustomSearch(items)
	addresses := make(map[string]int, len(items))
	for _, item := range items {
		addresses[item.Name]++
	}
	for i := range items {
		items[i].AddressConflict = addresses[items[i].Name] > 1
	}
	counts := collectionTitleCounts(items)
	for i := range items {
		if counts[strings.ToLower(items[i].Title)] > 1 && items[i].Description != "" && !strings.EqualFold(items[i].Description, items[i].Title) {
			items[i].Title += " — " + items[i].Description
		}
	}
	counts = collectionTitleCounts(items)
	for i := range items {
		if counts[strings.ToLower(items[i].Title)] > 1 {
			items[i].Title += " — " + items[i].Name
		}
	}
	counts = collectionTitleCounts(items)
	for i := range items {
		if counts[strings.ToLower(items[i].Title)] > 1 {
			items[i].Title += " — " + items[i].ID
		}
	}
	for i := range items {
		items[i].Kind, items[i].Topic = collectionReadingProfile(items[i])
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := strings.ToLower(items[i].Title), strings.ToLower(items[j].Title)
		if left == right {
			return items[i].ID < items[j].ID
		}
		return left < right
	})
	return items, nil
}

func collectionTitleCounts(items []localCollection) map[string]int {
	counts := make(map[string]int, len(items))
	for _, item := range items {
		counts[strings.ToLower(item.Title)]++
	}
	return counts
}

// Use a fixed number of workers, even when a library has many collections.
func searchCollectionWorkers(collections []localCollection, limit int, search func(localCollection)) {
	if limit > len(collections) {
		limit = len(collections)
	}
	jobs := make(chan localCollection)
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for collection := range jobs {
				search(collection)
			}
		}()
	}
	for _, collection := range collections {
		jobs <- collection
	}
	close(jobs)
	wg.Wait()
}

func (a *app) searchTitles(ctx context.Context, base, query string, collections []localCollection, start int) ([]localSearchResult, []string, bool) {
	results := make([]localSearchResult, 0)
	failures := make([]string, 0)
	hasMore := false
	var mu sync.Mutex
	searchCollectionWorkers(collections, 4, func(collection localCollection) {
		if ctx.Err() != nil {
			mu.Lock()
			failures = append(failures, collection.Title)
			mu.Unlock()
			return
		}
		u := base + "suggest?" + url.Values{"content": {collection.Name}, "term": {query}, "count": {"16"}, "start": {strconv.Itoa(start)}}.Encode()
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			mu.Lock()
			failures = append(failures, collection.Title)
			mu.Unlock()
			return
		}
		resp, err := a.client.Do(req)
		if err != nil {
			mu.Lock()
			failures = append(failures, collection.Title)
			mu.Unlock()
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			mu.Lock()
			failures = append(failures, collection.Title)
			mu.Unlock()
			return
		}
		var suggestions []struct {
			Value string `json:"value"`
			Path  string `json:"path"`
			Kind  string `json:"kind"`
		}
		if err = json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&suggestions); err != nil {
			mu.Lock()
			failures = append(failures, collection.Title)
			mu.Unlock()
			return
		}
		var matches []localSearchResult
		pathCount := 0
		more := false
		for _, s := range suggestions {
			if s.Kind != "path" || s.Path == "" {
				continue
			}
			pathCount++
			if pathCount > 15 {
				more = true
				continue
			}
			p := html.UnescapeString(s.Path)
			valid := true
			for _, part := range strings.Split(p, "/") {
				if part == ".." || part == "." {
					valid = false
					break
				}
			}
			if !valid || strings.HasPrefix(p, "/") {
				continue
			}
			u := url.URL{Path: "/content/" + collection.Name + "/" + p}
			matches = append(matches, localSearchResult{Title: html.UnescapeString(s.Value), Collection: collection.Title, URL: strings.TrimSuffix(base, "/") + u.EscapedPath(), rank: len(matches)})
		}
		mu.Lock()
		results = append(results, matches...)
		hasMore = hasMore || more
		mu.Unlock()
	})
	sort.Slice(results, func(i, j int) bool {
		if results[i].Collection == results[j].Collection {
			return results[i].rank < results[j].rank
		}
		return strings.ToLower(results[i].Collection) < strings.ToLower(results[j].Collection)
	})
	sort.Strings(failures)
	return results, failures, hasMore
}
func (a *app) librarySearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" || len(query) > 200 {
		http.Error(w, "Enter search text (up to 200 characters)", 400)
		return
	}
	id := r.URL.Query().Get("id")
	if id != "" && !uuidPattern.MatchString(id) {
		http.Error(w, "Invalid collection", 400)
		return
	}
	kind, topic := r.URL.Query().Get("kind"), r.URL.Query().Get("topic")
	if !validReadingFilters(kind, topic) {
		http.Error(w, "Invalid reading filter", 400)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "titles"
	}
	if mode != "titles" && mode != "fulltext" && mode != "combined" {
		http.Error(w, "Invalid search mode", 400)
		return
	}
	start := 0
	if raw := r.URL.Query().Get("start"); raw != "" {
		var err error
		start, err = strconv.Atoi(raw)
		if err != nil || start < 0 || start > 1000000 {
			http.Error(w, "Invalid search page", 400)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	items, err := a.localCollections(ctx)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	selected := make([]localCollection, 0, len(items))
	for _, item := range items {
		if (id == "" || item.ID == id) && matchesReadingFilters(item, kind, topic) {
			selected = append(selected, item)
		}
	}
	if len(selected) == 0 {
		if id == "" {
			http.Error(w, "No downloaded collections are available to search", 404)
		} else {
			http.Error(w, "Collection not found", 404)
		}
		return
	}
	base, err := a.localReaderURL()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	if mode == "titles" && start == 0 {
		fast := false
		for _, item := range selected {
			if item.IndexedTitles && !item.AddressConflict {
				fast = true
			}
		}
		if fast {
			copy := r.Clone(ctx)
			u := *r.URL
			values := u.Query()
			values.Set("field", "title")
			u.RawQuery = values.Encode()
			copy.URL = &u
			a.hybridLibrarySearch(w, copy)
			return
		}
	}
	skipped := make([]string, 0)
	conflicts := make([]string, 0)
	searchable := make([]localCollection, 0, len(selected))
	for _, item := range selected {
		if item.AddressConflict {
			conflicts = append(conflicts, item.Title+" (shared reader address)")
		} else if mode == "fulltext" && !item.FullText {
			skipped = append(skipped, item.Title)
		} else {
			searchable = append(searchable, item)
		}
	}
	selected = searchable
	var results []localSearchResult
	var failures []string
	var hasMore bool
	titleOnly := 0
	if mode == "combined" {
		indexed, titles := make([]localCollection, 0), make([]localCollection, 0)
		for _, item := range selected {
			if item.FullText {
				indexed = append(indexed, item)
			} else {
				titles = append(titles, item)
			}
		}
		fullResults, fullFailures, fullMore := a.searchFullText(ctx, base, query, indexed, start)
		titleResults, titleFailures, titleMore := a.searchTitles(ctx, base, query, titles, start)
		titleOnly = len(titles) - len(titleFailures)
		for i := range fullResults {
			fullResults[i].MatchMode = "fulltext"
		}
		for i := range titleResults {
			titleResults[i].MatchMode = "titles"
		}
		results = append(fullResults, titleResults...)
		failures = append(fullFailures, titleFailures...)
		hasMore = fullMore || titleMore
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].Collection == results[j].Collection {
				return results[i].rank < results[j].rank
			}
			return strings.ToLower(results[i].Collection) < strings.ToLower(results[j].Collection)
		})
	} else if mode == "fulltext" {
		results, failures, hasMore = a.searchFullText(ctx, base, query, selected, start)
	} else {
		results, failures, hasMore = a.searchTitles(ctx, base, query, selected, start)
	}
	searched := len(selected) - len(failures)
	failures = append(failures, conflicts...)
	sort.Strings(failures)
	writeJSON(w, map[string]any{"results": results, "unavailable": failures, "skipped": skipped, "titleOnly": titleOnly, "collectionsSearched": searched, "collectionsAttempted": len(selected) + len(conflicts), "perCollectionLimit": 15, "start": start, "hasMore": hasMore && start <= 999985})
}

// Kiwix requires matching languages for a single multi-ZIM query. Query each
// indexed collection separately, so mixed-language libraries remain searchable.
func (a *app) searchFullText(ctx context.Context, base, query string, collections []localCollection, start int) ([]localSearchResult, []string, bool) {
	results := make([]localSearchResult, 0)
	failures := make([]string, 0)
	hasMore := false
	var mu sync.Mutex
	searchCollectionWorkers(collections, 2, func(collection localCollection) {
		if ctx.Err() != nil {
			mu.Lock()
			failures = append(failures, collection.Title)
			mu.Unlock()
			return
		}
		u := base + "search?" + url.Values{"books.id": {collection.ID}, "pattern": {query}, "format": {"xml"}, "pageLength": {"16"}, "start": {strconv.Itoa(start)}}.Encode()
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			mu.Lock()
			failures = append(failures, collection.Title)
			mu.Unlock()
			return
		}
		resp, err := a.client.Do(req)
		if err != nil {
			mu.Lock()
			failures = append(failures, collection.Title)
			mu.Unlock()
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			mu.Lock()
			failures = append(failures, collection.Title)
			mu.Unlock()
			return
		}
		var feed struct {
			Channel struct {
				Items []struct {
					Title string `xml:"title"`
					Link  string `xml:"link"`
				} `xml:"item"`
			} `xml:"channel"`
		}
		if err = xml.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&feed); err != nil {
			mu.Lock()
			failures = append(failures, collection.Title)
			mu.Unlock()
			return
		}
		more := len(feed.Channel.Items) > 15
		if more {
			feed.Channel.Items = feed.Channel.Items[:15]
		}
		matches := make([]localSearchResult, 0, len(feed.Channel.Items))
		for _, item := range feed.Channel.Items {
			u, err := url.Parse(item.Link)
			if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/content/"+collection.Name+"/") {
				continue
			}
			valid := true
			for _, part := range strings.Split(u.Path, "/") {
				if part == ".." || part == "." {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			matches = append(matches, localSearchResult{Title: item.Title, Collection: collection.Title, URL: strings.TrimSuffix(base, "/") + u.EscapedPath(), rank: len(matches)})
		}
		mu.Lock()
		results = append(results, matches...)
		hasMore = hasMore || more
		mu.Unlock()
	})
	sort.Slice(results, func(i, j int) bool {
		if results[i].Collection == results[j].Collection {
			return results[i].rank < results[j].rank
		}
		return strings.ToLower(results[i].Collection) < strings.ToLower(results[j].Collection)
	})
	sort.Strings(failures)
	return results, failures, hasMore
}
