package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearchCollectionWorkersAreBounded(t *testing.T) {
	items := make([]localCollection, 1000)
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	finished := make(chan struct{})
	var processed atomic.Int32
	baseline := runtime.NumGoroutine()
	go func() {
		searchCollectionWorkers(items, 4, func(localCollection) {
			if processed.Add(1) <= 4 {
				started <- struct{}{}
				<-release
			}
		})
		close(finished)
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("workers did not start")
		}
	}
	additional := runtime.NumGoroutine() - baseline
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("workers did not finish")
	}
	if additional > 20 || processed.Load() != int32(len(items)) {
		t.Fatalf("workers not bounded or items missed: additional goroutines %d, processed %d", additional, processed.Load())
	}
}

func TestLocalTitleSearchAcrossCollections(t *testing.T) {
	catalog := `<feed><entry><id>urn:uuid:600c47c6-a792-0f78-8fcc-9c05cf1c556b</id><title>Books</title><summary>Library</summary><language>eng</language><tags>_ftindex:no</tags><link type="text/html" href="/content/books_2026-01"/></entry><entry><id>urn:uuid:ab7c8918-9940-f83d-9a14-7f64b4f55636</id><title>Guides</title><tags>_ftindex:yes</tags><link type="text/html" href="/content/guides_2026-01"/></entry></feed>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog/v2/entries":
			w.Write([]byte(catalog))
		case "/suggest":
			if r.URL.Query().Get("term") != "cheese" {
				t.Error("wrong search term")
			}
			if r.URL.Query().Get("content") == "books_2026-01" {
				w.Write([]byte(`[{"value":"A &amp; B","path":"A &amp; B_cover.1","kind":"path"},{"value":"bad","path":"../escape","kind":"path"}]`))
			} else {
				w.Write([]byte(`[{"value":"Cheese","path":"Cook/Cheese","kind":"path"}]`))
			}
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	a := &app{readerCmd: &exec.Cmd{}, readerURL: server.URL + "/", client: server.Client()}
	items, err := a.localCollections(context.Background())
	if err != nil || len(items) != 2 || items[0].FullText || !items[1].FullText {
		t.Fatalf("bad collections: %+v %v", items, err)
	}
	results, failures, _ := a.searchTitles(context.Background(), server.URL+"/", "cheese", items, 0)
	if len(failures) != 0 || len(results) != 2 {
		t.Fatalf("bad search: %+v %+v", results, failures)
	}
	u, err := url.Parse(results[0].URL)
	if err != nil || !strings.Contains(u.Path, "A & B_cover.1") || results[0].Title != "A & B" {
		t.Fatalf("wrong decoded result %+v", results[0])
	}
	b, _ := json.Marshal(results)
	if strings.Contains(string(b), "escape") {
		t.Fatal("path traversal returned")
	}
}
func TestSearchUnavailableCollectionReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "not available", http.StatusNotFound) }))
	defer server.Close()
	a := &app{client: server.Client()}
	results, failed, _ := a.searchTitles(context.Background(), server.URL+"/", "repair", []localCollection{{Name: "fixture", Title: "Fixture"}}, 0)
	if len(results) != 0 || len(failed) != 1 || failed[0] != "Fixture" {
		t.Fatalf("missing failure: %+v %+v", results, failed)
	}
}

func TestLibrarySearchReportsCompletedAndUnavailableCollections(t *testing.T) {
	catalog := `<feed><entry><id>urn:uuid:600c47c6-a792-0f78-8fcc-9c05cf1c556b</id><title>Available</title><link type="text/html" href="/content/available"/></entry><entry><id>urn:uuid:ab7c8918-9940-f83d-9a14-7f64b4f55636</id><title>Unavailable</title><link type="text/html" href="/content/unavailable"/></entry></feed>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog/v2/entries":
			fmt.Fprint(w, catalog)
		case "/suggest":
			if r.URL.Query().Get("content") == "unavailable" {
				http.Error(w, "unavailable", 503)
				return
			}
			fmt.Fprint(w, `[{"value":"Repair","path":"repair","kind":"path"}]`)
		}
	}))
	defer server.Close()
	a := &app{readerCmd: &exec.Cmd{}, readerURL: server.URL + "/", client: server.Client()}
	w := httptest.NewRecorder()
	a.librarySearch(w, httptest.NewRequest("GET", "/api/library/search?q=repair", nil))
	var result struct {
		Searched    int      `json:"collectionsSearched"`
		Attempted   int      `json:"collectionsAttempted"`
		Unavailable []string `json:"unavailable"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Searched != 1 || result.Attempted != 2 || len(result.Unavailable) != 1 || result.Unavailable[0] != "Unavailable" {
		t.Fatalf("incorrect search coverage: %d %s", w.Code, w.Body)
	}
}

func TestLibrarySearchExplainsEmptyLibrary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `<feed></feed>`) }))
	defer server.Close()
	a := &app{readerCmd: &exec.Cmd{}, readerURL: server.URL + "/", client: server.Client()}
	w := httptest.NewRecorder()
	a.librarySearch(w, httptest.NewRequest("GET", "/api/library/search?q=repair", nil))
	if w.Code != 404 || !strings.Contains(w.Body.String(), "No downloaded collections") {
		t.Fatalf("unclear empty library response: %d %s", w.Code, w.Body)
	}
}
func TestFullTextSearchAcrossIndexedCollections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("pattern") != "solar" || r.URL.Query().Get("format") != "xml" {
			t.Error("wrong search request")
		}
		if r.URL.Query().Get("books.id") == "bad" {
			http.Error(w, "failed", 500)
			return
		}
		w.Write([]byte(`<rss><channel><item><title>Solar &amp; wind</title><link>/content/guides_2026-01/Solar%20power</link><description>about &lt;solar&gt;</description></item><item><title>bad</title><link>https://evil.test/content/guides_2026-01/x</link></item></channel></rss>`))
	}))
	defer server.Close()
	a := &app{client: server.Client()}
	results, failed, _ := a.searchFullText(context.Background(), server.URL+"/", "solar", []localCollection{{ID: "good", Name: "guides_2026-01", Title: "Guides"}, {ID: "bad", Name: "bad", Title: "Broken"}}, 0)
	if len(results) != 1 || results[0].Title != "Solar & wind" || len(failed) != 1 || failed[0] != "Broken" {
		t.Fatalf("bad full-text results: %+v failures: %+v", results, failed)
	}
	u, err := url.Parse(results[0].URL)
	if err != nil || u.Path != "/content/guides_2026-01/Solar power" {
		t.Fatalf("bad result URL: %+v %v", u, err)
	}
}
func TestLocalSearchPreservesReaderRanking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"value":"Z relevant","path":"z","kind":"path"},{"value":"A less relevant","path":"a","kind":"path"}]`))
	}))
	defer server.Close()
	a := &app{client: server.Client()}
	results, failed, _ := a.searchTitles(context.Background(), server.URL+"/", "test", []localCollection{{Name: "fixture", Title: "Fixture"}}, 0)
	if len(failed) != 0 || len(results) != 2 || results[0].Title != "Z relevant" {
		t.Fatalf("search ranking lost: %+v %+v", results, failed)
	}
}
func TestDuplicateCollectionTitlesAreDistinguished(t *testing.T) {
	data := `<feed><entry><id>urn:uuid:600c47c6-a792-0f78-8fcc-9c05cf1c556b</id><title>Books</title><summary>Science</summary><link type="text/html" href="/content/science"/></entry><entry><id>urn:uuid:ab7c8918-9940-f83d-9a14-7f64b4f55636</id><title>Books</title><summary>History</summary><link type="text/html" href="/content/history"/></entry></feed>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(data)) }))
	defer server.Close()
	a := &app{readerCmd: &exec.Cmd{}, readerURL: server.URL + "/", client: server.Client()}
	items, err := a.localCollections(context.Background())
	if err != nil || len(items) != 2 || items[0].Title != "Books — History" || items[1].Title != "Books — Science" {
		t.Fatalf("ambiguous titles: %+v %v", items, err)
	}
	if items[0].IndexStatus != "unknown" || !items[0].FullText {
		t.Fatal("unknown index status misclassified")
	}
}

func TestDuplicateTitlesWithoutDistinctDescriptionsUseArchiveName(t *testing.T) {
	data := `<feed><entry><id>urn:uuid:600c47c6-a792-0f78-8fcc-9c05cf1c556b</id><title>Books</title><summary>Same description</summary><link type="text/html" href="/content/books_a"/></entry><entry><id>urn:uuid:ab7c8918-9940-f83d-9a14-7f64b4f55636</id><title>Books</title><summary>Same description</summary><link type="text/html" href="/content/books_b"/></entry></feed>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog/v2/entries" {
			fmt.Fprint(w, data)
			return
		}
		fmt.Fprint(w, `[{"value":"First","path":"first","kind":"path"},{"value":"Second","path":"second","kind":"path"}]`)
	}))
	defer server.Close()
	a := &app{readerCmd: &exec.Cmd{}, readerURL: server.URL + "/", client: server.Client()}
	items, err := a.localCollections(context.Background())
	if err != nil || len(items) != 2 || items[0].Title != "Books — Same description — books_a" || items[1].Title != "Books — Same description — books_b" {
		t.Fatalf("ambiguous labels: %+v %v", items, err)
	}
	results, failures, _ := a.searchTitles(context.Background(), server.URL+"/", "book", items, 0)
	if len(failures) != 0 || len(results) != 4 || results[0].Collection != items[0].Title || results[1].Collection != items[0].Title || results[2].Collection != items[1].Title || results[3].Collection != items[1].Title || results[0].Title != "First" || results[1].Title != "Second" {
		t.Fatalf("search results not distinguished and grouped: %+v %v", results, failures)
	}
}

func TestDuplicateArchiveNamesUseCollectionID(t *testing.T) {
	data := `<feed><entry><id>urn:uuid:600c47c6-a792-0f78-8fcc-9c05cf1c556b</id><title>Books</title><link type="text/html" href="/content/books"/></entry><entry><id>urn:uuid:ab7c8918-9940-f83d-9a14-7f64b4f55636</id><title>Books</title><link type="text/html" href="/content/books"/></entry></feed>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, data) }))
	defer server.Close()
	a := &app{readerCmd: &exec.Cmd{}, readerURL: server.URL + "/", client: server.Client()}
	items, err := a.localCollections(context.Background())
	if err != nil || len(items) != 2 || items[0].Title == items[1].Title || !strings.HasSuffix(items[0].Title, items[0].ID) || !strings.HasSuffix(items[1].Title, items[1].ID) || !items[0].AddressConflict || !items[1].AddressConflict {
		t.Fatalf("ambiguous labels for same archive name: %+v %v", items, err)
	}
}

func TestSearchReportsSharedReaderAddressWithoutMisattributedResults(t *testing.T) {
	data := `<feed><entry><id>urn:uuid:600c47c6-a792-0f78-8fcc-9c05cf1c556b</id><title>First</title><tags>_ftindex:yes</tags><link type="text/html" href="/content/shared"/></entry><entry><id>urn:uuid:ab7c8918-9940-f83d-9a14-7f64b4f55636</id><title>Second</title><tags>_ftindex:yes</tags><link type="text/html" href="/content/shared"/></entry><entry><id>urn:uuid:12345678-1234-1234-1234-123456789abc</id><title>Other</title><tags>_ftindex:yes</tags><link type="text/html" href="/content/other"/></entry></feed>`
	var unsafeRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog/v2/entries":
			fmt.Fprint(w, data)
		case "/suggest":
			if r.URL.Query().Get("content") != "other" {
				unsafeRequests.Add(1)
			}
			fmt.Fprint(w, `[{"value":"Result","path":"page","kind":"path"}]`)
		case "/search":
			if r.URL.Query().Get("books.id") != "12345678-1234-1234-1234-123456789abc" {
				unsafeRequests.Add(1)
			}
			fmt.Fprint(w, `<rss><channel><item><title>Result</title><link>/content/other/page</link></item></channel></rss>`)
		}
	}))
	defer server.Close()
	a := &app{readerCmd: &exec.Cmd{}, readerURL: server.URL + "/", client: server.Client()}
	for _, mode := range []string{"titles", "fulltext", "combined"} {
		for _, id := range []string{"", "600c47c6-a792-0f78-8fcc-9c05cf1c556b"} {
			params := url.Values{"q": {"test"}, "mode": {mode}, "id": {id}}
			w := httptest.NewRecorder()
			a.librarySearch(w, httptest.NewRequest("GET", "/api/library/search?"+params.Encode(), nil))
			var result struct {
				Results     []localSearchResult `json:"results"`
				Unavailable []string            `json:"unavailable"`
				Searched    int                 `json:"collectionsSearched"`
				Attempted   int                 `json:"collectionsAttempted"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
				t.Fatalf("%s: %d %s", mode, w.Code, w.Body)
			}
			wantResults, wantUnavailable := 1, 2
			if id != "" {
				wantResults, wantUnavailable = 0, 1
			}
			if len(result.Results) != wantResults || len(result.Unavailable) != wantUnavailable || result.Searched != wantResults || result.Attempted != wantResults+wantUnavailable || !strings.Contains(result.Unavailable[0], "shared reader address") {
				t.Fatalf("%s %s: incorrect coverage: %s", mode, id, w.Body)
			}
		}
	}
	if unsafeRequests.Load() != 0 {
		t.Fatal("searched a shared reader address")
	}
}

func TestCombinedSearchUsesTitlesOnlyForUnindexedCollections(t *testing.T) {
	data := `<feed><entry><id>urn:uuid:600c47c6-a792-0f78-8fcc-9c05cf1c556b</id><title>Indexed</title><tags>_ftindex:yes</tags><link type="text/html" href="/content/indexed"/></entry><entry><id>urn:uuid:ab7c8918-9940-f83d-9a14-7f64b4f55636</id><title>Titles</title><tags>_ftindex:no</tags><link type="text/html" href="/content/titles"/></entry></feed>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog/v2/entries":
			fmt.Fprint(w, data)
		case "/search":
			if r.URL.Query().Get("books.id") != "600c47c6-a792-0f78-8fcc-9c05cf1c556b" {
				t.Error("wrong full-text collection")
			}
			fmt.Fprint(w, `<rss><channel><item><title>Body result</title><link>/content/indexed/page</link></item></channel></rss>`)
		case "/suggest":
			if r.URL.Query().Get("content") != "titles" {
				t.Error("wrong title collection")
			}
			fmt.Fprint(w, `[{"value":"Title result","path":"page","kind":"path"}]`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	a := &app{readerCmd: &exec.Cmd{}, readerURL: server.URL + "/", client: server.Client()}
	w := httptest.NewRecorder()
	a.librarySearch(w, httptest.NewRequest("GET", "/api/library/search?q=bicycle+repair&mode=combined", nil))
	var result struct {
		Results   []localSearchResult `json:"results"`
		Searched  int                 `json:"collectionsSearched"`
		TitleOnly int                 `json:"titleOnly"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Searched != 2 || result.TitleOnly != 1 || len(result.Results) != 2 || result.Results[0].MatchMode != "fulltext" || result.Results[1].MatchMode != "titles" {
		t.Fatalf("unexpected combined results: %d %s", w.Code, w.Body)
	}
}

func TestSearchPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := r.URL.Query().Get("start")
		switch r.URL.Path {
		case "/suggest":
			if r.URL.Query().Get("count") != "16" {
				t.Error("missing lookahead")
			}
			var data []map[string]string
			first, n := 0, 16
			if start == "15" {
				first, n = 15, 3
			} else if start != "0" {
				t.Errorf("unexpected start %q", start)
			}
			for i := first; i < first+n; i++ {
				data = append(data, map[string]string{"value": fmt.Sprintf("Title %02d", i), "path": fmt.Sprintf("page%d", i), "kind": "path"})
			}
			json.NewEncoder(w).Encode(data)
		case "/search":
			if r.URL.Query().Get("pageLength") != "16" {
				t.Error("missing lookahead")
			}
			first, n := 0, 16
			if start == "15" {
				first, n = 15, 3
			} else if start != "0" {
				t.Errorf("unexpected start %q", start)
			}
			fmt.Fprint(w, "<rss><channel>")
			for i := first; i < first+n; i++ {
				fmt.Fprintf(w, "<item><title>Title %02d</title><link>/content/fixture/page%d</link></item>", i, i)
			}
			fmt.Fprint(w, "</channel></rss>")
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()
	a := &app{client: server.Client()}
	items := []localCollection{{ID: "fixture", Name: "fixture", Title: "Fixture"}}
	for _, search := range []func(context.Context, string, string, []localCollection, int) ([]localSearchResult, []string, bool){a.searchTitles, a.searchFullText} {
		first, failed, more := search(context.Background(), server.URL+"/", "test", items, 0)
		if len(failed) != 0 || len(first) != 15 || !more || first[14].Title != "Title 14" {
			t.Fatalf("bad first page: %+v, %v, %v", first, failed, more)
		}
		second, failed, more := search(context.Background(), server.URL+"/", "test", items, 15)
		if len(failed) != 0 || len(second) != 3 || more || second[0].Title != "Title 15" {
			t.Fatalf("bad second page: %+v, %v, %v", second, failed, more)
		}
	}
}

func TestSearchCancellationReachesReader(t *testing.T) {
	for _, mode := range []string{"titles", "fulltext"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			a := &app{client: server.Client()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				items := []localCollection{{ID: "fixture", Name: "fixture", Title: "Fixture"}}
				if mode == "titles" {
					a.searchTitles(ctx, server.URL+"/", "test", items, 0)
				} else {
					a.searchFullText(ctx, server.URL+"/", "test", items, 0)
				}
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("reader request did not start")
			}
			cancel()
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("reader request was not canceled")
			}
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("search did not finish after cancellation")
			}
		})
	}
}

func TestSearchCancellationAccountsForQueuedCollections(t *testing.T) {
	for _, mode := range []string{"titles", "fulltext"} {
		t.Run(mode, func(t *testing.T) {
			limit := 4
			if mode == "fulltext" {
				limit = 2
			}
			started := make(chan struct{}, limit)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started <- struct{}{}
				<-r.Context().Done()
			}))
			defer server.Close()
			a := &app{client: server.Client()}
			items := make([]localCollection, 40)
			for i := range items {
				items[i] = localCollection{ID: fmt.Sprint(i), Name: fmt.Sprint(i), Title: fmt.Sprint(i)}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan []string, 1)
			go func() {
				var failures []string
				if mode == "titles" {
					_, failures, _ = a.searchTitles(ctx, server.URL+"/", "test", items, 0)
				} else {
					_, failures, _ = a.searchFullText(ctx, server.URL+"/", "test", items, 0)
				}
				finished <- failures
			}()
			for i := 0; i < limit; i++ {
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					cancel()
					t.Fatal("reader requests did not start")
				}
			}
			cancel()
			select {
			case failures := <-finished:
				if len(failures) != len(items) {
					t.Fatalf("lost queued collections after cancellation: %d failures, want %d", len(failures), len(items))
				}
			case <-time.After(3 * time.Second):
				t.Fatal("search did not finish after cancellation")
			}
		})
	}
}
