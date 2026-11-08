package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	search "offline-library-kit/desktop/internal/librarysearch"
	"offline-library-kit/desktop/internal/zimfixture"
)

func TestSearchBuildsAgainstRepairedIdentity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	name := "ifixit_test.zim"
	source := filepath.Join(dir, name)
	if err := zimfixture.Write(source); err != nil {
		t.Fatal(err)
	}
	original, err := search.Identify(source)
	if err != nil {
		t.Fatal(err)
	}
	original.Collection = "old-ifixit"
	assets := t.TempDir()
	for _, kind := range []string{"catalog", "custom"} {
		err = search.Build(ctx, filepath.Join(assets, kind), original, "Original iFixit", func(emit func(search.Document) error) error {
			return emit(search.Document{Path: "Guide/Correct/123", Title: "Old source", Text: "old publisher text"})
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	a := &app{root: root, searchDir: assets}
	if err = a.repairIFixit(ctx, downloadRequest{Name: name}); err != nil {
		t.Fatal(err)
	}
	if err = a.prepareCollectionSearch(ctx, downloadRequest{Name: name}); err != nil {
		t.Fatal(err)
	}
	id, err := search.Identify(filepath.Join(dir, repairedName(name)))
	if err != nil {
		t.Fatal(err)
	}
	if id.UUID == original.UUID {
		t.Fatal("repair did not create a new identity")
	}
	en := search.NewLibrary()
	defer en.Close()
	if _, _, match, err := en.Query(ctx, filepath.Join(assets, "custom"), id, "old", "deep"); err != nil || match {
		t.Fatal("old pack incorrectly rebound", err)
	}
	pack := filepath.Join(a.searchRoot(), "local", localSearchName(id))
	count, docs, match, err := en.Query(ctx, pack, id, "unicorn instructions", "deep")
	if err != nil || !match || count != 1 || len(docs) != 1 || docs[0].Path != "Guide/Correct/123" {
		t.Fatalf("%d %+v %t %v", count, docs, match, err)
	}
	before, err := os.Stat(filepath.Join(pack, "index.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.prepareCollectionSearch(ctx, downloadRequest{Name: name}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(pack, "index.bin"))
	if !os.SameFile(before, after) {
		t.Fatal("rebuilt a compatible local index")
	}
	if matches, _ := filepath.Glob(filepath.Join(a.searchRoot(), "local", ".search-source-*.part")); len(matches) != 0 {
		t.Fatal("successful build retained full-text spool")
	}
	// The existing app API exposes the new index through the reader's real alias.
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/v2/entries" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<feed><entry><id>urn:uuid:%s</id><title>iFixit</title><tags>_ftindex:no</tags><language>eng</language><link type="text/html" href="/content/actual-reader-alias"/></entry></feed>`, id.UUID)
	}))
	defer reader.Close()
	a.readerCmd = &exec.Cmd{}
	a.readerURL = reader.URL + "/"
	a.client = reader.Client()
	a.token = "test"
	req := httptest.NewRequest("GET", "http://127.0.0.1:9999/api/library/hybrid?q=unicorn+instructions", nil)
	req.Header.Set("X-Library-Token", "test")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, req)
	var result struct {
		Results             []localSearchResult
		Total               int
		CollectionsSearched int
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Results) != 1 || result.Total != 1 || result.CollectionsSearched != 1 {
		t.Fatalf("%d %s %v", w.Code, w.Body, err)
	}
	if result.Results[0].URL != reader.URL+"/content/actual-reader-alias/Guide/Correct/123" {
		t.Fatal(result.Results[0].URL)
	}
	// The existing Titles only control also uses the binary index when bound.
	fast := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "http://127.0.0.1:9999/api/library/search?q=Correct+123&mode=titles", nil)
	request.Header.Set("X-Library-Token", "test")
	a.handler().ServeHTTP(fast, request)
	var indexed struct {
		Hybrid  bool
		Results []localSearchResult
	}
	if err = json.Unmarshal(fast.Body.Bytes(), &indexed); err != nil || fast.Code != 200 || !indexed.Hybrid || len(indexed.Results) != 1 {
		t.Fatalf("%d %s %v", fast.Code, fast.Body, err)
	}
	if a.searchEngine != nil {
		a.searchEngine.Close()
	}
}

func TestSearchAssetCopyRetainsConflicts(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "manifest.json"), []byte("new content"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "content", "search")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dest, "manifest.json")
	if err := os.WriteFile(name, []byte("keep original"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{root: root}
	if err := a.copySearchTree(context.Background(), src, dest, 2); err == nil {
		t.Fatal("conflicting index was replaced")
	}
	got, _ := os.ReadFile(name)
	if string(got) != "keep original" {
		t.Fatal("original removed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.copySearchTree(ctx, src, filepath.Join(root, "canceled"), 2); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestHybridRealLibrary(t *testing.T) {
	sources, reader := os.Getenv("OLK_SEARCH_REAL_SOURCES"), os.Getenv("OLK_SEARCH_REAL_READER")
	if sources == "" || reader == "" {
		t.Skip("real search integration inputs not supplied")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".zim" {
			if err := os.Symlink(filepath.Join(sources, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	a := &app{root: root, searchDir: filepath.Join("..", "search-indexes"), readerURL: reader + "/", readerCmd: &exec.Cmd{}, client: http.DefaultClient}
	defer func() {
		if a.searchEngine != nil {
			a.searchEngine.Close()
		}
	}()
	for _, q := range []struct{ query, field, id string }{{"fermentation", "deep", "438c7985-7b24-b470-9dfc-7b0cafa02269"}, {"battery", "deep", "97b9c3e1-51db-324d-9608-0da874fc7ab0"}, {"solar panel", "deep", ""}, {"cheese", "all", ""}} {
		values := url.Values{"q": {q.query}, "field": {q.field}, "id": {q.id}}
		w := httptest.NewRecorder()
		a.hybridLibrarySearch(w, httptest.NewRequest("GET", "http://127.0.0.1:9999/api/library/hybrid?"+values.Encode(), nil))
		var result struct {
			Results     []localSearchResult
			Total       int
			Notice      string
			Unavailable []string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Results) == 0 || len(result.Unavailable) > 0 {
			t.Fatalf("%s: %d %s %v", q.query, w.Code, w.Body, err)
		}
		t.Logf("%s (%s): %d results, total %d, notice %q", q.query, q.field, len(result.Results), result.Total, result.Notice)
	}
}

func TestSearchRealGoRepairedArchive(t *testing.T) {
	source, root := os.Getenv("OLK_SEARCH_GO_REPAIRED"), os.Getenv("OLK_SEARCH_BUILD_OUTPUT")
	if source == "" || root == "" {
		t.Skip("large repaired archive integration inputs not supplied")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	name := "ifixit-test" + repairedSuffix
	if err := os.Link(source, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	a := &app{root: root, searchDir: filepath.Join("..", "search-indexes")}
	if err := a.prepareCollectionSearch(context.Background(), downloadRequest{Name: name}); err != nil {
		t.Fatal(err)
	}
	id, err := search.Identify(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(a.searchRoot(), "local", localSearchName(id))
	en := search.NewLibrary()
	defer en.Close()
	n, docs, match, err := en.Query(context.Background(), pack, id, "battery replacement", "deep")
	if err != nil || !match || n == 0 || len(docs) == 0 {
		t.Fatalf("%d %d %t %v", n, len(docs), match, err)
	}
	m, err := search.ReadManifest(pack)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Repaired archive index verified: %d pages, %d bytes, %d matches", m.Documents, m.Stats.IndexBytes, n)
}
