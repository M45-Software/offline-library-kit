package main

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestReadingProfilesAndExactFilterValidation(t *testing.T) {
	for _, test := range []struct{ name, kind, topic string }{{"gutenberg_en_lcc-g", "books", "books"}, {"gutenberg agriculture", "books", "food"}, {"ifixit repair", "guides", "repair"}, {"cooking.stackexchange", "questions", "food"}, {"PhET", "interactive", "learning"}, {"maps USA", "maps", "travel"}, {"Wikipedia", "reference", "reference"}} {
		item := localCollection{Name: test.name}
		kind, topic := collectionReadingProfile(item)
		if kind != test.kind || topic != test.topic || !matchesReadingFilters(item, kind, topic) || !matchesReadingFilters(item, "", "") {
			t.Fatalf("bad reading profile: %s %s %s", test.name, kind, topic)
		}
	}
	for _, filter := range []string{"book", "books|guides", "invalid", "|"} {
		if validReadingFilters(filter, "") {
			t.Fatalf("invalid kind accepted %s", filter)
		}
	}
	a := &app{root: t.TempDir()}
	for _, path := range []string{"/api/library/search?q=water&kind=invalid", "/api/library/hybrid?q=water&topic=invalid"} {
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:9999"+path, nil))
		if w.Code != 400 {
			t.Fatalf("filter accepted: %d %s", w.Code, w.Body)
		}
	}
}
func TestReadingFilterAppliedBeforeTitleSearch(t *testing.T) {
	var searched []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog/v2/entries" {
			w.Write([]byte(`<feed><entry><id>urn:uuid:600c47c6-a792-0f78-8fcc-9c05cf1c556b</id><title>Gutenberg books</title><tags>_ftindex:no</tags><link type="text/html" href="/content/gutenberg_2026"/></entry><entry><id>urn:uuid:ab7c8918-9940-f83d-9a14-7f64b4f55636</id><title>iFixit</title><link type="text/html" href="/content/ifixit_2026"/></entry></feed>`))
			return
		}
		if r.URL.Path == "/suggest" {
			searched = append(searched, r.URL.Query().Get("content"))
			w.Write([]byte(`[{"value":"Water pump","path":"pump","kind":"path"}]`))
			return
		}
		t.Errorf("unexpected request %s", r.URL)
	}))
	defer server.Close()
	a := &app{readerCmd: &exec.Cmd{}, readerURL: server.URL + "/", client: server.Client()}
	w := httptest.NewRecorder()
	a.librarySearch(w, httptest.NewRequest("GET", "http://127.0.0.1:9999/api/library/search?q=water&mode=titles&kind=guides&topic=repair", nil))
	if w.Code != 200 || len(searched) != 1 || !strings.HasPrefix(searched[0], "ifixit_") || strings.Contains(w.Body.String(), "Gutenberg books") {
		t.Fatalf("filter applied too late: %v %d %s", searched, w.Code, w.Body)
	}
}
