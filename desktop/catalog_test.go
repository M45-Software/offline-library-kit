package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func TestCatalogSearchAndPublisherMetadata(t *testing.T) {
	id := "600c47c6-a792-0f78-8fcc-9c05cf1c556b"
	entry := `<entry><id>urn:uuid:` + id + `</id><title>Example</title><summary>Useful content</summary><language>eng</language><updated>2026-01-01</updated><link rel="http://opds-spec.org/acquisition/open-access" type="application/x-zim" href="https://lb.download.kiwix.org/zim/example/example.zim.meta4" length="1024"/></entry>`
	c := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "library.kiwix.org" && r.URL.Path == "/catalog/v2/entries":
			if r.URL.Query().Get("q") != "science" || r.URL.Query().Get("lang") != "eng" {
				t.Errorf("unexpected query %v", r.URL)
			}
			return response(`<feed><totalResults>1</totalResults>` + entry + `</feed>`), nil
		case r.URL.Host == "library.kiwix.org" && strings.HasSuffix(r.URL.Path, id):
			return response(entry), nil
		case r.URL.Host == "download.kiwix.org":
			return response(`<metalink><file name="example.zim"><size>1000</size><hash type="sha-1">ignore</hash><hash type="sha-256">` + strings.Repeat("a", 64) + `</hash></file></metalink>`), nil
		default:
			t.Fatalf("unexpected request %v", r.URL)
			return nil, nil
		}
	})}
	items, total, err := searchCatalog(context.Background(), c, kiwixCatalog, "science", "eng", 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].ApproxBytes != 1024 {
		t.Fatalf("unexpected catalog: %+v %d %v", items, total, err)
	}
	d, err := resolveCatalogItem(context.Background(), c, kiwixCatalog, id)
	if err != nil || d.Bytes != 1000 || d.Name != "example.zim" || d.URL != "https://download.kiwix.org/zim/example/example.zim" || d.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("bad metadata: %+v %v", d, err)
	}
}
func TestCatalogRejectsUnsafeMetadata(t *testing.T) {
	for _, u := range []string{"http://download.kiwix.org/zim/a.zim.meta4", "https://download.kiwix.org.evil.test/zim/a.zim.meta4", "https://evil.test/zim/a.zim.meta4", "https://download.kiwix.org/zim/../secret.zim.meta4", "https://download.kiwix.org/zim/a.zim.meta4?x=1"} {
		if _, err := canonicalMetalink(u); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
	entry := catalogEntry{ID: "urn:uuid:600c47c6-a792-0f78-8fcc-9c05cf1c556b", Links: []catalogLink{{Type: "application/x-zim", Rel: "http://opds-spec.org/acquisition/open-access", Href: "https://evil.test/zim/a.zim.meta4"}}}
	if _, ok := catalogItemFor(entry); ok {
		t.Fatal("accepted untrusted catalog link")
	}
}
